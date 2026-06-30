package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// CLIName is the binary name of the Harness unified CLI.
const CLIName = "harness"

// CLIClient drives the `harness` binary instead of REST. Auth, account, base
// URL all come from the active profile (~/.harness/credentials), so unlike
// REST we need no env vars when a profile is configured. Org/project are
// passed per-command from the parsed RepoRef so a single profile can serve
// any repo the user has access to.
//
// Surface matches *Client so Host can take either via the prBackend
// interface.
type CLIClient struct {
	bin    string
	runCmd func(ctx context.Context, name string, args ...string) *exec.Cmd
}

// NewCLIClient returns a CLIClient. cmd builds an *exec.Cmd in the caller's
// workdir + env (same pattern as github/gitlab hosts). When nil, exec.CommandContext
// is used directly.
func NewCLIClient(cmd func(ctx context.Context, name string, args ...string) *exec.Cmd) *CLIClient {
	if cmd == nil {
		cmd = func(ctx context.Context, name string, args ...string) *exec.Cmd {
			return exec.CommandContext(ctx, name, args...)
		}
	}
	return &CLIClient{bin: CLIName, runCmd: cmd}
}

// CLIAvailable reports whether the `harness` binary is resolvable on PATH.
func CLIAvailable() bool {
	_, err := exec.LookPath(CLIName)
	return err == nil
}

func (c *CLIClient) scopeArgs(repo RepoRef) []string {
	args := make([]string, 0, 4)
	if repo.Org != "" {
		args = append(args, "--org", repo.Org)
	}
	if repo.Project != "" {
		args = append(args, "--project", repo.Project)
	}
	return args
}

func (c *CLIClient) run(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	cmd := c.runCmd(ctx, c.bin, args...)
	if len(stdin) > 0 {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("harness %s: %s", strings.Join(args, " "), msg)
	}
	return stdout.Bytes(), nil
}

// Available verifies the binary is on PATH and a profile resolves. We treat
// any non-zero exit from `harness auth status` as "not configured" — same
// contract as gh/glab.
func (c *CLIClient) Available(ctx context.Context) error {
	if !CLIAvailable() {
		return errors.New("harness CLI is not installed")
	}
	if _, err := c.run(ctx, nil, "auth", "status"); err != nil {
		return fmt.Errorf("harness CLI is not authenticated: %w", err)
	}
	return nil
}

// FindOpenPRBySourceBranch lists open PRs and filters in-process by source
// branch — the `list pr` command doesn't expose a source-branch query flag.
// Caller-supplied target_branch is also filtered locally.
func (c *CLIClient) FindOpenPRBySourceBranch(ctx context.Context, repo RepoRef, source, target string) (*PullRequest, error) {
	args := append([]string{"list", "pr", repo.Repo, "--state", "open", "--json", "--raw"}, c.scopeArgs(repo)...)
	out, err := c.run(ctx, nil, args...)
	if err != nil {
		return nil, err
	}
	prs, err := decodeListPRs(out)
	if err != nil {
		return nil, err
	}
	source = strings.TrimSpace(source)
	target = strings.TrimSpace(target)
	for _, pr := range prs {
		if source != "" && pr.SourceBranch != source {
			continue
		}
		if target != "" && pr.TargetBranch != target {
			continue
		}
		match := pr
		return &match, nil
	}
	return nil, nil
}

// CreatePR opens a PR via `harness create pr <repo> --set ...`. Description
// is piped on stdin via `-f -` so newlines and shell-special chars are safe;
// `--set` values can't carry multi-line bodies reliably.
func (c *CLIClient) CreatePR(ctx context.Context, repo RepoRef, source, target, title, body string) (*PullRequest, error) {
	args := []string{
		"create", "pr", repo.Repo,
		"--json",
		"--set", "title=" + title,
		"--set", "source_branch=" + source,
		"--set", "target_branch=" + target,
	}
	args = append(args, c.scopeArgs(repo)...)
	var stdin []byte
	if strings.TrimSpace(body) != "" {
		args = append(args, "-f", "-")
		stdin = []byte(body)
	}
	out, err := c.run(ctx, stdin, args...)
	if err != nil {
		return nil, err
	}
	return decodeSinglePR(out)
}

// UpdatePR sets title + description on an existing PR.
func (c *CLIClient) UpdatePR(ctx context.Context, repo RepoRef, number int, title, body string) (*PullRequest, error) {
	id := fmt.Sprintf("%s/%d", repo.Repo, number)
	args := []string{
		"update", "pr", id,
		"--json",
		"--set", "title=" + title,
		"--set", "description=" + body,
	}
	args = append(args, c.scopeArgs(repo)...)
	out, err := c.run(ctx, nil, args...)
	if err != nil {
		return nil, err
	}
	return decodeSinglePR(out)
}

// GetPR fetches PR state by number.
func (c *CLIClient) GetPR(ctx context.Context, repo RepoRef, number int) (*PullRequest, error) {
	id := fmt.Sprintf("%s/%d", repo.Repo, number)
	args := []string{"get", "pr", id, "--json", "--raw"}
	args = append(args, c.scopeArgs(repo)...)
	out, err := c.run(ctx, nil, args...)
	if err != nil {
		return nil, err
	}
	return decodeSinglePR(out)
}

// UIBase returns the UI host for synthesizing PR URLs. The CLI doesn't print
// a UI URL alongside the JSON, so we derive one from the active profile's
// api_url via `harness auth status --json`.
func (c *CLIClient) UIBase() string {
	st, err := c.authStatus(context.Background())
	if err != nil || strings.TrimSpace(st.APIUrl) == "" {
		return defaultBaseURL
	}
	return strings.TrimRight(st.APIUrl, "/")
}

// AccountID returns the account id resolved by the active CLI profile, or ""
// if it can't be determined.
func (c *CLIClient) AccountID(ctx context.Context) string {
	st, err := c.authStatus(ctx)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(st.AccountID)
}

type authStatus struct {
	APIUrl    string `json:"APIUrl"`
	AccountID string `json:"AccountID"`
}

func (c *CLIClient) authStatus(ctx context.Context) (authStatus, error) {
	out, err := c.run(ctx, nil, "auth", "status", "--json")
	if err != nil {
		return authStatus{}, err
	}
	var st authStatus
	if err := json.Unmarshal(out, &st); err != nil {
		return authStatus{}, err
	}
	return st, nil
}

func decodeSinglePR(out []byte) (*PullRequest, error) {
	trimmed := bytes.TrimSpace(out)
	if len(trimmed) == 0 {
		return nil, errors.New("empty harness CLI response")
	}
	var pr PullRequest
	if err := json.Unmarshal(trimmed, &pr); err != nil {
		return nil, fmt.Errorf("decode harness CLI PR: %w", err)
	}
	return &pr, nil
}

func decodeListPRs(out []byte) ([]PullRequest, error) {
	trimmed := bytes.TrimSpace(out)
	if len(trimmed) == 0 {
		return nil, nil
	}
	var prs []PullRequest
	if err := json.Unmarshal(trimmed, &prs); err != nil {
		return nil, fmt.Errorf("decode harness CLI PR list: %w", err)
	}
	return prs, nil
}
