package harness

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

type fakeCmdCall struct {
	args   []string
	stdout string
	stderr string
	exit   int
}

func newFakeCLIClient(calls *[]*fakeCmdCall, scripts map[string]*fakeCmdCall) *CLIClient {
	return &CLIClient{
		bin: "harness",
		runCmd: func(ctx context.Context, name string, args ...string) *exec.Cmd {
			key := strings.Join(append([]string{name}, args[:min(2, len(args))]...), " ")
			script, ok := scripts[key]
			if !ok {
				script = &fakeCmdCall{stdout: "{}"}
			}
			*calls = append(*calls, &fakeCmdCall{args: append([]string{}, args...), stdout: script.stdout, stderr: script.stderr, exit: script.exit})
			shell := "printf %s " + shellQuote(script.stdout)
			if script.stderr != "" {
				shell += "; printf %s " + shellQuote(script.stderr) + " 1>&2"
			}
			if script.exit != 0 {
				shell += "; exit " + strconv.Itoa(script.exit)
			}
			return exec.CommandContext(ctx, "/bin/sh", "-c", shell)
		},
	}
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func TestCLIClient_CreatePR(t *testing.T) {
	var calls []*fakeCmdCall
	scripts := map[string]*fakeCmdCall{
		"harness create pr": {stdout: `{"number":7,"state":"open","source_branch":"feat","target_branch":"main","title":"hi"}`},
	}
	c := newFakeCLIClient(&calls, scripts)
	repo := RepoRef{Account: "ACCT", Org: "ORG", Project: "PROJ", Repo: "REPO"}
	pr, err := c.CreatePR(context.Background(), repo, "feat", "main", "hi", "body text")
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}
	if pr.Number != 7 {
		t.Fatalf("Number = %d, want 7", pr.Number)
	}
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
	args := calls[0].args
	want := []string{"create", "pr", "REPO", "--json", "--set", "title=hi", "--set", "source_branch=feat", "--set", "target_branch=main", "--org", "ORG", "--project", "PROJ", "-f", "-"}
	if strings.Join(args, " ") != strings.Join(want, " ") {
		t.Fatalf("args mismatch:\n got: %v\nwant: %v", args, want)
	}
}

func TestCLIClient_FindFiltersBySource(t *testing.T) {
	body := `[{"number":1,"source_branch":"x","target_branch":"main","state":"open"},
	{"number":2,"source_branch":"feat","target_branch":"main","state":"open"}]`
	scripts := map[string]*fakeCmdCall{"harness list pr": {stdout: body}}
	var calls []*fakeCmdCall
	c := newFakeCLIClient(&calls, scripts)
	repo := RepoRef{Org: "O", Project: "P", Repo: "R"}
	pr, err := c.FindOpenPRBySourceBranch(context.Background(), repo, "feat", "main")
	if err != nil {
		t.Fatalf("FindOpenPRBySourceBranch: %v", err)
	}
	if pr == nil || pr.Number != 2 {
		t.Fatalf("got %+v, want PR #2", pr)
	}
}

func TestCLIClient_RunError(t *testing.T) {
	scripts := map[string]*fakeCmdCall{"harness get pr": {stderr: "API error 404: Not Found", exit: 1}}
	var calls []*fakeCmdCall
	c := newFakeCLIClient(&calls, scripts)
	_, err := c.GetPR(context.Background(), RepoRef{Org: "O", Project: "P", Repo: "R"}, 5)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Fatalf("err = %v, want 404 mention", err)
	}
}
