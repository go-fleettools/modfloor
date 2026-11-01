package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// ⛔ The list is the join between the measurement and the act: the campaign runs
// through `gopin --from X --version Y --gomod --list FILE`, and nobody should
// retype 191 slugs. `--declares` is a single floor because `gopin --from` takes
// a single floor — anything looser hands the campaign a list its tool refuses to
// act on, one repository at a time, after the budget is spent.
func TestReposPrintsOneSlugPerRepositoryForOneDeclaredFloor(t *testing.T) {
	rows := []Row{
		{Module: "github.com/go-x/a", Repo: Repo{Host: "github.com", Owner: "go-x", Name: "a"},
			Published: Floors{Go: "1.27.1"}, Verdict: Below},
		// A second module of the SAME repository: one entry, or gopin edits it
		// twice and opens two pull requests for one change.
		{Module: "github.com/go-x/a/tools", Repo: Repo{Host: "github.com", Owner: "go-x", Name: "a"},
			Published: Floors{Go: "1.27.1"}, Verdict: Below},
		// No `go` directive at all, so its group is "(none)" and not 1.27.1.
		// (The first version of this fixture gave it a published floor AND a
		// no-go verdict, which cannot both be true, and the contradiction read
		// as a bug in the code.)
		{Module: "github.com/go-x/b", Repo: Repo{Host: "github.com", Owner: "go-x", Name: "b"},
			Verdict: NoFloor},
		// Another floor: not this run's.
		{Module: "github.com/go-x/c", Repo: Repo{Host: "github.com", Owner: "go-x", Name: "c"},
			Published: Floors{Go: "1.26.4"}, Verdict: Below},
		// Not exposed: not work.
		{Module: "github.com/go-x/d", Repo: Repo{Host: "github.com", Owner: "go-x", Name: "d"},
			Published: Floors{Go: "1.27.3"}, Verdict: OK},
		// No work tree, so no repository to push a fix to.
		{Module: "example.com/elsewhere", Published: Floors{Go: "1.27.1"}, Verdict: Below},
	}

	var out strings.Builder
	n, err := reposOf(&out, rows, "1.27.1", false)
	if err != nil {
		t.Fatal(err)
	}
	// go-x/b declares no directive at all, so it is not in the 1.27.1 group.
	if n != 1 || out.String() != "go-x/a\n" {
		t.Fatalf("n=%d out=%q", n, out.String())
	}

	// Without --declares, every exposed module's repository, deduplicated and
	// sorted so the file is stable between runs.
	out.Reset()
	if n, err = reposOf(&out, rows, "", false); err != nil {
		t.Fatal(err)
	}
	if n != 3 || out.String() != "go-x/a\ngo-x/b\ngo-x/c\n" {
		t.Fatalf("n=%d out=%q", n, out.String())
	}

	// A module with no directive is its own group, named "(none)", because
	// `--from` has nothing to match there.
	out.Reset()
	if n, err = reposOf(&out, rows, "(none)", false); err != nil {
		t.Fatal(err)
	}
	if n != 1 || out.String() != "go-x/b\n" {
		t.Fatalf("n=%d out=%q", n, out.String())
	}
}

// ⛔ An empty list is not an empty campaign. Printing nothing and exiting 0
// would read as "done", so a `--declares` that matches nothing is an error that
// NAMES the floors which do occur.
func TestAnUnmatchedDeclaresNamesTheFloorsThatExist(t *testing.T) {
	root := t.TempDir()
	fakeRepo(t, filepath.Join(root, "one"), "go-fleettools/one")
	write(t, filepath.Join(root, "one", "go.mod"), "module github.com/go-fleettools/one\n\ngo 1.27.1\n")

	o := offlineOpts(root, "1.27.3", ParseOwners("go-fleettools"))
	o.repos, o.declares = true, "1.22"
	var out strings.Builder
	code, _, err := reportModules(&out, &out, o)
	if code != 2 || err == nil {
		t.Fatalf("code=%d err=%v out=%q", code, err, out.String())
	}
	if !strings.Contains(err.Error(), "1.27.1 (1)") {
		t.Errorf("the refusal must name the floors that occur: %v", err)
	}
	if out.String() != "" {
		t.Errorf("nothing must be printed when the list is empty: %q", out.String())
	}

	// And the matching case prints the slug alone — no table above it, because
	// the output is meant to be redirected into gopin's --list file.
	o.declares = "1.27.1"
	out.Reset()
	if code, _, err = reportModules(&out, &out, o); code != 0 || err != nil {
		t.Fatalf("code=%d err=%v", code, err)
	}
	if out.String() != "go-fleettools/one\n" {
		t.Fatalf("out=%q — a table in the list file is a list gopin cannot read", out.String())
	}
}
