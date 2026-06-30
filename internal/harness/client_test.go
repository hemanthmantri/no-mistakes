package harness

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseRepoRef(t *testing.T) {
	cases := []struct {
		raw  string
		want RepoRef
		ok   bool
	}{
		{"https://git.harness.io/acct/orgX/proj/repo.git", RepoRef{"acct", "orgX", "proj", "repo"}, true},
		{"https://git.harness.io/acct/orgX/proj/repo", RepoRef{"acct", "orgX", "proj", "repo"}, true},
		{
			"https://git0.harness.io/l7B_kbSEQD2wjrM7PShm5w/PROD/Harness_Commons/harness-ti.git",
			RepoRef{"l7B_kbSEQD2wjrM7PShm5w", "PROD", "Harness_Commons", "harness-ti"},
			true,
		},
		{"https://app.harness.io/gateway/code/git/acct/orgX/proj/repo.git", RepoRef{"acct", "orgX", "proj", "repo"}, true},
		{"https://app.harness.io/gateway/code/git/Acct/orgX/proj/repo", RepoRef{"Acct", "orgX", "proj", "repo"}, true},
		{"https://git.harness.io/onlytwo/parts", RepoRef{}, false},
		{"", RepoRef{}, false},
	}
	for _, tc := range cases {
		got, err := ParseRepoRef(tc.raw)
		if tc.ok && err != nil {
			t.Errorf("ParseRepoRef(%q) unexpected err: %v", tc.raw, err)
			continue
		}
		if !tc.ok {
			if err == nil {
				t.Errorf("ParseRepoRef(%q) expected error, got %+v", tc.raw, got)
			}
			continue
		}
		if got != tc.want {
			t.Errorf("ParseRepoRef(%q) = %+v, want %+v", tc.raw, got, tc.want)
		}
	}
}

func TestCompareURL_MatchesPostPushHint(t *testing.T) {
	// The exact URL Harness Code prints in its post-push "Create a pull
	// request" hint for git0.harness.io.
	gitURL := "https://git0.harness.io/l7B_kbSEQD2wjrM7PShm5w/PROD/Harness_Commons/harness-ti.git"
	ref, err := ParseRepoRef(gitURL)
	if err != nil {
		t.Fatalf("ParseRepoRef: %v", err)
	}
	got := CompareURL(gitURL, ref, "testing-no-mistakes", "main")
	want := "https://harness0.harness.io/ng/account/l7B_kbSEQD2wjrM7PShm5w/module/code/orgs/PROD/projects/Harness_Commons/repos/harness-ti/pulls/compare/main...testing-no-mistakes"
	if got != want {
		t.Errorf("CompareURL mismatch:\n got: %s\nwant: %s", got, want)
	}
}

func TestUIBaseFromGitURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://git.harness.io/a/b/c/d.git", "https://app.harness.io"},
		{"https://git0.harness.io/a/b/c/d.git", "https://harness0.harness.io"},
		{"https://git7.harness.io/a/b/c/d.git", "https://harness7.harness.io"},
		{"https://code.acme-harness.internal/a/b/c/d.git", "https://code.acme-harness.internal"},
	}
	for _, tc := range cases {
		if got := UIBaseFromGitURL(tc.in); got != tc.want {
			t.Errorf("UIBaseFromGitURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestAccountFromPAT(t *testing.T) {
	if got := accountFromPAT("pat.ACCT123.xxx.yyy"); got != "ACCT123" {
		t.Errorf("accountFromPAT pat: got %q want ACCT123", got)
	}
	if got := accountFromPAT("sat.ACCT.tok.rest"); got != "ACCT" {
		t.Errorf("accountFromPAT sat: got %q want ACCT", got)
	}
	if got := accountFromPAT("not-a-pat"); got != "" {
		t.Errorf("accountFromPAT junk: got %q want \"\"", got)
	}
}

func TestNewClientFromEnv_DerivesAccountFromPAT(t *testing.T) {
	t.Setenv(EnvAPIKey, "")
	t.Setenv(EnvAccountID, "")
	env := []string{"HARNESS_API_KEY=pat.DERIVED.tok.rest"}
	c, err := NewClientFromEnv(env)
	if err != nil {
		t.Fatalf("NewClientFromEnv: %v", err)
	}
	if c.account != "DERIVED" {
		t.Fatalf("account = %q, want DERIVED", c.account)
	}
}

func TestClient_CreateAndFindPR(t *testing.T) {
	var lastReq *http.Request
	var lastBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastReq = r
		if r.Method == http.MethodPost {
			data, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(data, &lastBody)
			_, _ = w.Write([]byte(`{"number":42,"state":"open","source_branch":"feat","target_branch":"main"}`))
			return
		}
		// GET -> list
		_, _ = w.Write([]byte(`[{"number":42,"state":"open","source_branch":"feat","target_branch":"main"}]`))
	}))
	defer srv.Close()

	env := []string{
		"HARNESS_API_KEY=pat.ACCT.tok.rest",
		"HARNESS_BASE_URL=" + srv.URL,
	}
	c, err := NewClientFromEnv(env)
	if err != nil {
		t.Fatalf("NewClientFromEnv: %v", err)
	}
	repo := RepoRef{Account: "ACCT", Org: "ORG", Project: "PROJ", Repo: "REPO"}
	ctx := context.Background()

	pr, err := c.CreatePR(ctx, repo, "feat", "main", "title", "body")
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}
	if pr.Number != 42 {
		t.Errorf("pr.Number = %d, want 42", pr.Number)
	}
	// Verify auth headers + scope query params.
	if got := lastReq.Header.Get("x-api-key"); got != "pat.ACCT.tok.rest" {
		t.Errorf("x-api-key = %q", got)
	}
	if got := lastReq.URL.Query().Get("accountIdentifier"); got != "ACCT" {
		t.Errorf("accountIdentifier = %q", got)
	}
	if got := lastReq.URL.Query().Get("routingId"); got != "ACCT" {
		t.Errorf("routingId = %q", got)
	}
	if got := lastReq.URL.Query().Get("orgIdentifier"); got != "ORG" {
		t.Errorf("orgIdentifier = %q", got)
	}
	if got := lastBody["source_branch"]; got != "feat" {
		t.Errorf("source_branch body = %v", got)
	}
	// Path must contain the encoded org/project/repo segment.
	if !strings.Contains(lastReq.URL.Path, "/code/api/v1/repos/") {
		t.Errorf("path = %q, want /code/api/v1/repos/ prefix", lastReq.URL.Path)
	}

	found, err := c.FindOpenPRBySourceBranch(ctx, repo, "feat", "main")
	if err != nil || found == nil || found.Number != 42 {
		t.Fatalf("FindOpenPRBySourceBranch: pr=%+v err=%v", found, err)
	}
	if got := lastReq.URL.Query().Get("source_branch"); got != "feat" {
		t.Errorf("list source_branch = %q", got)
	}
	if got := lastReq.URL.Query().Get("state"); got != "open" {
		t.Errorf("list state = %q", got)
	}
}
