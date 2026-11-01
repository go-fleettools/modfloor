package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// ⛔ A campaign is costed in PULL REQUESTS, not in findings. Three exposed
// modules of one repository are one pull request, because the `go` directives
// they declare land together; counting the modules gives an estimate that is a
// multiple of the real one, and an estimate three times too large is an estimate
// that gets the campaign refused.
func TestTheCampaignIsCountedInRepositoriesNotModules(t *testing.T) {
	rows := []Row{
		{Module: "github.com/go-x/a", Repo: Repo{Owner: "go-x", Name: "a"}, Verdict: Below},
		{Module: "github.com/go-x/a/tools", Repo: Repo{Owner: "go-x", Name: "a"}, Verdict: Below},
		{Module: "github.com/go-x/a/v2", Repo: Repo{Owner: "go-x", Name: "a"}, Verdict: NoFloor},
		{Module: "github.com/go-x/b", Repo: Repo{Owner: "go-x", Name: "b"}, Verdict: Below},
		// Not exposed, so not work: it must not inflate the estimate.
		{Module: "github.com/go-x/c", Repo: Repo{Owner: "go-x", Name: "c"}, Verdict: OK},
		// Nor this one, which fails CLOSED under GOTOOLCHAIN=local.
		{Module: "github.com/go-x/d", Repo: Repo{Owner: "go-x", Name: "d"}, Verdict: Pinned},
		// Named as a positional argument: no work tree, so no repository to
		// credit it to. Counted apart rather than folded into the estimate.
		{Module: "example.com/elsewhere", Verdict: Below},
	}
	repos, unplaced := campaign(rows)
	if repos != 2 {
		t.Errorf("repositories = %d, want 2 — five exposed modules, two repositories", repos)
	}
	if unplaced != 1 {
		t.Errorf("unplaced = %d, want 1", unplaced)
	}
	if r, u := campaign(nil); r != 0 || u != 0 {
		t.Errorf("campaign(nil) = %d, %d; want zeros", r, u)
	}
}

// And it reaches the report, where the person deciding whether to run the
// campaign will read it.
func TestTheReportStatesTheCostInPullRequests(t *testing.T) {
	root := t.TempDir()
	one := filepath.Join(root, "one")
	fakeRepo(t, one, "go-fleettools/one")
	write(t, filepath.Join(one, "go.mod"), "module github.com/go-fleettools/one\n\ngo 1.27.1\n")
	write(t, filepath.Join(one, "tools", "go.mod"), "module github.com/go-fleettools/one/tools\n\ngo 1.27.1\n")
	two := filepath.Join(root, "two")
	fakeRepo(t, two, "go-fleettools/two")
	write(t, filepath.Join(two, "go.mod"), "module github.com/go-fleettools/two\n\ngo 1.27.1\n")

	var out strings.Builder
	code, c, err := reportModules(&out, &out, offlineOpts(root, "1.27.3", ParseOwners("go-fleettools")))
	if err != nil {
		t.Fatal(err)
	}
	if code != 1 || c.Modules != 3 {
		t.Fatalf("code=%d kept=%d, want 1 and 3\n%s", code, c.Modules, out.String())
	}
	if !strings.Contains(out.String(), "they live in 2 repositories, so the campaign is about 2 pull requests and not 3.") {
		t.Error("the report must cost the campaign in pull requests:\n" + out.String())
	}
}
