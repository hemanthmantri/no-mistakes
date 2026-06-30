package harness

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/kunchenguid/no-mistakes/internal/scm"
)

// prBackend is the subset of Harness PR operations Host needs. *Client (REST)
// and *CLIClient (`harness` binary) both satisfy it.
type prBackend interface {
	Available(ctx context.Context) error
	FindOpenPRBySourceBranch(ctx context.Context, repo RepoRef, source, target string) (*PullRequest, error)
	CreatePR(ctx context.Context, repo RepoRef, source, target, title, body string) (*PullRequest, error)
	UpdatePR(ctx context.Context, repo RepoRef, number int, title, body string) (*PullRequest, error)
	GetPR(ctx context.Context, repo RepoRef, number int) (*PullRequest, error)
	UIBase() string
}

// Host implements scm.Host for Harness Code. CI/checks/mergeability are
// out-of-scope in this pass and return scm.ErrUnsupported.
type Host struct {
	client prBackend
	repo   RepoRef
}

func NewHost(client prBackend, repo RepoRef) *Host { return &Host{client: client, repo: repo} }

func (h *Host) Provider() scm.Provider { return scm.ProviderHarness }

func (h *Host) Capabilities() scm.Capabilities { return scm.Capabilities{} }

func (h *Host) Available(ctx context.Context) error {
	if h == nil || h.client == nil {
		return errors.New("Harness client is not configured")
	}
	return h.client.Available(ctx)
}

func (h *Host) FindPR(ctx context.Context, branch, base string) (*scm.PR, error) {
	pr, err := h.client.FindOpenPRBySourceBranch(ctx, h.repo, branch, base)
	if err != nil {
		return nil, err
	}
	if pr == nil {
		return nil, nil
	}
	return h.toSCM(pr), nil
}

func (h *Host) CreatePR(ctx context.Context, branch, base string, content scm.PRContent) (*scm.PR, error) {
	pr, err := h.client.CreatePR(ctx, h.repo, branch, base, content.Title, content.Body)
	if err != nil {
		return nil, err
	}
	return h.toSCM(pr), nil
}

func (h *Host) UpdatePR(ctx context.Context, pr *scm.PR, content scm.PRContent) (*scm.PR, error) {
	id, err := strconv.Atoi(pr.Number)
	if err != nil {
		return nil, fmt.Errorf("invalid Harness PR number %q: %w", pr.Number, err)
	}
	updated, err := h.client.UpdatePR(ctx, h.repo, id, content.Title, content.Body)
	if err != nil {
		return nil, err
	}
	return h.toSCM(updated), nil
}

func (h *Host) GetPRState(ctx context.Context, pr *scm.PR) (scm.PRState, error) {
	id, err := strconv.Atoi(pr.Number)
	if err != nil {
		return "", err
	}
	got, err := h.client.GetPR(ctx, h.repo, id)
	if err != nil {
		return "", err
	}
	if got == nil {
		return "", nil
	}
	return normalizePRState(got.State), nil
}

// GetChecks/GetMergeableState/FetchFailedCheckLogs: CI integration is a
// follow-up. The CI step gates on Capabilities and on the build-host skip
// reason, so returning ErrUnsupported here is the contracted "not supported"
// path.
func (h *Host) GetChecks(_ context.Context, _ *scm.PR) ([]scm.Check, error) {
	return nil, scm.ErrUnsupported
}

func (h *Host) GetMergeableState(_ context.Context, _ *scm.PR) (scm.MergeableState, error) {
	return "", scm.ErrUnsupported
}

func (h *Host) FetchFailedCheckLogs(_ context.Context, _ *scm.PR, _, _ string, _ []string) (string, error) {
	return "", scm.ErrUnsupported
}

func (h *Host) toSCM(pr *PullRequest) *scm.PR {
	// Harness Code does not return an html_url on pullreq responses, so we
	// derive it from the configured UI base + repo path.
	url := fmt.Sprintf("%s/ng/account/%s/module/code/orgs/%s/projects/%s/repos/%s/pulls/%d",
		h.client.UIBase(), h.repo.Account, h.repo.Org, h.repo.Project, h.repo.Repo, pr.Number)
	return &scm.PR{
		Number: strconv.Itoa(pr.Number),
		URL:    url,
	}
}

// CompareURL returns the Harness Code "create a pull request" UI URL for the
// given source/target branches. It needs no API credentials — useful when
// HARNESS_API_KEY is not set and we want to surface a link the user can click.
// The UI host is derived from gitURL so sharded SaaS installs (git0 ->
// harness0) get the right host.
func CompareURL(gitURL string, repo RepoRef, source, target string) string {
	return fmt.Sprintf("%s/ng/account/%s/module/code/orgs/%s/projects/%s/repos/%s/pulls/compare/%s...%s",
		UIBaseFromGitURL(gitURL), repo.Account, repo.Org, repo.Project, repo.Repo, target, source)
}

func normalizePRState(raw string) scm.PRState {
	switch raw {
	case "open":
		return scm.PRStateOpen
	case "merged":
		return scm.PRStateMerged
	case "closed":
		return scm.PRStateClosed
	default:
		return scm.PRState(raw)
	}
}
