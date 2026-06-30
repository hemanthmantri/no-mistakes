// Package harness is a minimal REST client for Harness Code Repositories.
// It implements the scm.Host PR surface (find/create/update/state). CI
// monitoring (checks, mergeability, log fetching) is intentionally
// out-of-scope for this pass: the matching scm.Host methods return
// scm.ErrUnsupported and Capabilities advertises no optional features.
package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	defaultBaseURL = "https://app.harness.io"

	EnvAPIKey    = "HARNESS_API_KEY"
	EnvAccountID = "HARNESS_ACCOUNT_ID"
	EnvBaseURL   = "HARNESS_BASE_URL"
	EnvOrg       = "HARNESS_ORG"
	EnvProject   = "HARNESS_PROJECT"
)

// RepoRef identifies a Harness Code repository inside an account/org/project.
// The Repo field is the slug; the full repoRef path segment used by the API
// is "<org>/<project>/<repo>".
type RepoRef struct {
	Account string
	Org     string
	Project string
	Repo    string
}

func (r RepoRef) repoRefPath() string {
	// API encodes "/" inside the repoRef segment as %2F.
	raw := r.Org + "/" + r.Project + "/" + r.Repo
	return url.PathEscape(raw)
}

// PullRequest is the subset of the Harness pullreq response we use.
type PullRequest struct {
	Number       int    `json:"number"`
	Title        string `json:"title"`
	State        string `json:"state"`
	IsDraft      bool   `json:"is_draft"`
	SourceBranch string `json:"source_branch"`
	TargetBranch string `json:"target_branch"`
	SourceSHA    string `json:"source_sha"`
}

// Client talks to Harness Code via REST. It deliberately knows nothing about
// CI/pipelines yet — only the pullreq surface.
type Client struct {
	baseURL string
	apiKey  string
	account string
	http    *http.Client
}

// NewClientFromEnv reads HARNESS_API_KEY (required) plus optional overrides
// from the daemon env slice falling back to process env. Account id is
// auto-derived from the "pat.<accountId>...." PAT prefix when not set.
// When env vars are unset, falls back to `harness auth token` / `auth status`
// for the active CLI profile (so users who ran `harness auth login` don't
// need to also export HARNESS_API_KEY).
func NewClientFromEnv(env []string) (*Client, error) {
	apiKey := strings.TrimSpace(lookupEnv(env, EnvAPIKey))
	account := strings.TrimSpace(lookupEnv(env, EnvAccountID))
	baseURL := strings.TrimSpace(lookupEnv(env, EnvBaseURL))

	if apiKey == "" {
		tok, acct, api, err := cliAuthFallback()
		if err != nil {
			return nil, fmt.Errorf("%s is not set and `harness auth token` fallback failed: %w (run `harness auth login` or export %s)", EnvAPIKey, err, EnvAPIKey)
		}
		apiKey = tok
		if account == "" {
			account = acct
		}
		if baseURL == "" {
			baseURL = api
		}
	}
	if account == "" {
		account = accountFromPAT(apiKey)
	}
	if account == "" {
		return nil, fmt.Errorf("missing %s and could not derive account from %s prefix", EnvAccountID, EnvAPIKey)
	}
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		account: account,
		http:    &http.Client{Timeout: 30 * time.Second},
	}, nil
}

// cliAuthFallback runs `harness auth token` + `harness auth status --json` to
// recover credentials when env vars are unset. Returns (token, account, apiURL).
func cliAuthFallback() (string, string, string, error) {
	if _, err := exec.LookPath(CLIName); err != nil {
		return "", "", "", fmt.Errorf("harness CLI not on PATH")
	}
	tokOut, err := exec.Command(CLIName, "auth", "token").Output()
	if err != nil {
		return "", "", "", fmt.Errorf("`harness auth token`: %w", err)
	}
	token := strings.TrimSpace(string(tokOut))
	if token == "" {
		return "", "", "", fmt.Errorf("`harness auth token` returned empty token")
	}
	statusOut, err := exec.Command(CLIName, "auth", "status", "--json").Output()
	if err != nil {
		return token, "", "", nil // token is enough; account will derive from PAT
	}
	var st struct {
		AccountID string `json:"AccountID"`
		APIUrl    string `json:"APIUrl"`
	}
	_ = json.Unmarshal(statusOut, &st)
	return token, strings.TrimSpace(st.AccountID), strings.TrimSpace(st.APIUrl), nil
}

// accountFromPAT extracts the account id from a Harness PAT of the form
// "pat.<accountId>.<rest>". Returns "" when the format doesn't match.
func accountFromPAT(pat string) string {
	parts := strings.SplitN(pat, ".", 3)
	if len(parts) < 2 {
		return ""
	}
	prefix := strings.ToLower(parts[0])
	if prefix != "pat" && prefix != "sat" {
		return ""
	}
	return parts[1]
}

// ParseRepoRef accepts an HTTPS clone URL or an app.harness.io UI URL and
// extracts <account>/<org>/<project>/<repo>. The .git suffix is optional.
//
// Supported shapes (case-insensitive host match):
//   - https://git.harness.io/<acct>/<org>/<project>/<repo>[.git]
//   - https://app.harness.io/gateway/code/git/<acct>/<org>/<project>/<repo>[.git]
//   - https://<custom-host>/<acct>/<org>/<project>/<repo>[.git] when HARNESS_HOST matches.
func ParseRepoRef(raw string) (RepoRef, error) {
	trimmed := strings.TrimSuffix(strings.TrimSpace(raw), ".git")
	if trimmed == "" {
		return RepoRef{}, errors.New("empty Harness repo URL")
	}
	u, err := url.Parse(trimmed)
	if err != nil || u.Host == "" {
		return RepoRef{}, fmt.Errorf("parse Harness repo URL: %w", err)
	}
	parts := splitPath(u.Path)
	// app.harness.io/gateway/code/git/<acct>/<org>/<project>/<repo>
	if len(parts) >= 3 && strings.EqualFold(parts[0], "gateway") && strings.EqualFold(parts[1], "code") && strings.EqualFold(parts[2], "git") {
		parts = parts[3:]
	}
	if len(parts) < 4 {
		return RepoRef{}, fmt.Errorf("invalid Harness repo path %q (want <account>/<org>/<project>/<repo>)", u.Path)
	}
	return RepoRef{
		Account: parts[0],
		Org:     parts[1],
		Project: parts[2],
		Repo:    parts[3],
	}, nil
}

func splitPath(p string) []string {
	out := make([]string, 0, 8)
	for _, seg := range strings.Split(p, "/") {
		if seg = strings.TrimSpace(seg); seg != "" {
			out = append(out, seg)
		}
	}
	return out
}

// Available reports whether REST API credentials are wired up. The
// constructor already validates the API key + account, so a non-nil Client
// is implicitly ready.
func (c *Client) Available(_ context.Context) error {
	if c == nil || c.apiKey == "" || c.account == "" {
		return errors.New("Harness REST client is not configured")
	}
	return nil
}

// FindOpenPRBySourceBranch returns the first open PR with the given source
// (and optionally target) branch, or nil when none exist.
func (c *Client) FindOpenPRBySourceBranch(ctx context.Context, repo RepoRef, source, target string) (*PullRequest, error) {
	q := url.Values{}
	q.Set("state", "open")
	if s := strings.TrimSpace(source); s != "" {
		q.Set("source_branch", s)
	}
	if t := strings.TrimSpace(target); t != "" {
		q.Set("target_branch", t)
	}
	var resp []PullRequest
	if err := c.do(ctx, http.MethodGet, c.pullreqPath(repo), repo, q, nil, &resp); err != nil {
		return nil, err
	}
	if len(resp) == 0 {
		return nil, nil
	}
	pr := resp[0]
	return &pr, nil
}

// CreatePR opens a new pull request.
func (c *Client) CreatePR(ctx context.Context, repo RepoRef, source, target, title, body string) (*PullRequest, error) {
	reqBody := map[string]any{
		"title":         title,
		"description":   body,
		"source_branch": source,
		"target_branch": target,
		"is_draft":      false,
	}
	var resp PullRequest
	if err := c.do(ctx, http.MethodPost, c.pullreqPath(repo), repo, nil, reqBody, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// UpdatePR updates title and description on an existing PR.
func (c *Client) UpdatePR(ctx context.Context, repo RepoRef, number int, title, body string) (*PullRequest, error) {
	reqBody := map[string]any{
		"title":       title,
		"description": body,
	}
	path := fmt.Sprintf("%s/%d", c.pullreqPath(repo), number)
	var resp PullRequest
	if err := c.do(ctx, http.MethodPatch, path, repo, nil, reqBody, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// GetPR fetches a single PR by number.
func (c *Client) GetPR(ctx context.Context, repo RepoRef, number int) (*PullRequest, error) {
	path := fmt.Sprintf("%s/%d", c.pullreqPath(repo), number)
	var resp PullRequest
	if err := c.do(ctx, http.MethodGet, path, repo, nil, nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) pullreqPath(repo RepoRef) string {
	return fmt.Sprintf("/code/api/v1/repos/%s/pullreq", repo.repoRefPath())
}

// UIBase returns the UI host derived from baseURL. UI links are rendered on
// app.harness.io even when the API base points at a gateway path.
func (c *Client) UIBase() string {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return defaultBaseURL
	}
	return u.Scheme + "://" + u.Host
}

// UIBaseFromGitURL returns the UI host paired with a Harness Code git URL.
// Mapping observed on SaaS:
//   - git.harness.io   -> app.harness.io
//   - gitN.harness.io  -> harnessN.harness.io (sharded; the value Harness
//     prints in its post-push "Create a pull request" hint).
//
// Anything else (self-hosted, etc.) is returned as scheme://host unchanged.
func UIBaseFromGitURL(gitURL string) string {
	u, err := url.Parse(strings.TrimSpace(gitURL))
	if err != nil || u.Host == "" {
		return defaultBaseURL
	}
	scheme := u.Scheme
	if scheme == "" {
		scheme = "https"
	}
	host := strings.ToLower(u.Host)
	switch {
	case host == "git.harness.io":
		return scheme + "://app.harness.io"
	case strings.HasPrefix(host, "git") && strings.HasSuffix(host, ".harness.io"):
		return scheme + "://harness" + strings.TrimPrefix(host, "git")
	default:
		return scheme + "://" + host
	}
}

func (c *Client) do(ctx context.Context, method, path string, repo RepoRef, query url.Values, reqBody any, out any) error {
	q := url.Values{}
	for k, v := range query {
		q[k] = v
	}
	// Account/routing identifiers are query params on every request.
	q.Set("accountIdentifier", c.account)
	q.Set("routingId", c.account)
	if repo.Org != "" {
		q.Set("orgIdentifier", repo.Org)
	}
	if repo.Project != "" {
		q.Set("projectIdentifier", repo.Project)
	}

	endpoint := c.baseURL + path
	if encoded := q.Encode(); encoded != "" {
		endpoint += "?" + encoded
	}

	var body io.Reader = http.NoBody
	if reqBody != nil {
		payload, err := json.Marshal(reqBody)
		if err != nil {
			return fmt.Errorf("marshal Harness request body: %w", err)
		}
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return fmt.Errorf("build Harness request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("Harness-Account", c.account)
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("Harness %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("Harness %s %s: status %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode Harness response: %w", err)
	}
	return nil
}

func lookupEnv(env []string, key string) string {
	prefix := key + "="
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			return strings.TrimPrefix(entry, prefix)
		}
	}
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return ""
}
