package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ⛔⛔ THE INCIDENT THIS GUARD EXISTS FOR. On 2026-11-01 the first real baseline
// of this fleet came back holding `sigs.k8s.io/yaml`, `cel.dev/expr` and twenty
// more, every one of them found inside `tannevaled/pantry` — a repository that
// carries other projects' sources. Each `remote.origin.url` said tannevaled, so
// each one passed the owner test, and nobody here can raise the floor of
// `sigs.k8s.io/yaml`. The same shape as the 532-modules incident, through the
// other door: that one was go.mod files in NO repository, this one is foreign
// go.mod files in OURS.
func TestAForeignModuleInOurWorkTreeIsNotOurs(t *testing.T) {
	ours := Repo{Host: "github.com", Owner: "tannevaled", Name: "pantry"}
	for _, c := range []struct {
		module string
		want   bool
		why    string
	}{
		{"github.com/tannevaled/pantry", true, "the repository's own module"},
		{"github.com/tannevaled/pantry/tools", true, "a module in a subdirectory of it"},
		{"github.com/tannevaled/pantry/v2", true, "a major-version suffix"},
		{"sigs.k8s.io/yaml", false, "a vanity host can never be a forge slug"},
		{"cel.dev/expr", false, "likewise"},
		{"github.com/tannevaled/other", false, "another repository of the same owner"},
		{"github.com/elsewhere/pantry", false, "the same name under another owner"},
		{"bench", false, "a path no consumer could ever resolve"},
		{"github.com/tannevaled/pantry-extra", false, "a PREFIX is not a path element"},
	} {
		if got := ours.Declares(c.module); got != c.want {
			t.Errorf("Declares(%q) = %v, want %v — %s", c.module, got, c.want, c.why)
		}
	}

	// With no remote there is nothing to compare against, and the owner globs
	// have already had their say: left alone rather than guessed at.
	if !(Repo{Root: "/x"}).Declares("sigs.k8s.io/yaml") {
		t.Error("a work tree with no remote must not be judged on a module path")
	}
}

// And the same thing through the walk, counted and named rather than silently
// dropped: a population is only honest when what it set aside is printed beside
// what it kept.
func TestTheWalkSetsAsideAForeignModuleAndCountsIt(t *testing.T) {
	root := t.TempDir()
	fakeRepo(t, filepath.Join(root, "pantry"), "tannevaled/pantry")
	// The repository's own module, below the floor: a real exposure.
	write(t, filepath.Join(root, "pantry", "go.mod"),
		"module github.com/tannevaled/pantry\n\ngo 1.27.1\n")
	// Two other projects' sources, carried inside it. Both below the floor,
	// and neither of them ours to raise.
	for dir, module := range map[string]string{"yaml": "sigs.k8s.io/yaml", "expr": "cel.dev/expr"} {
		d := filepath.Join(root, "pantry", "srcs", dir)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(d, "go.mod"), "module "+module+"\n\ngo 1.22\n")
	}

	var out, errOut strings.Builder
	o := offlineOpts(root, "1.27.3", ParseOwners("tannevaled"))
	o.verbose = true
	code, c, err := reportModules(&out, &errOut, o)
	if err != nil {
		t.Fatal(err)
	}
	if code != 1 {
		t.Fatalf("the repository's own module is below the floor: want 1, got %d\n%s", code, out.String())
	}
	if c.Modules != 1 || c.Foreign != 2 {
		t.Fatalf("kept %d modules and set aside %d foreign ones, want 1 and 2\n%s", c.Modules, c.Foreign, out.String())
	}
	if strings.Contains(out.String(), "sigs.k8s.io/yaml") || strings.Contains(out.String(), "cel.dev/expr") {
		t.Error("a module we cannot fix reached the report:\n" + out.String())
	}
	if !strings.Contains(out.String(), "2 published by another project") {
		t.Error("the population line must say what it set aside:\n" + out.String())
	}
	if !strings.Contains(errOut.String(), "which tannevaled/pantry does not publish") {
		t.Error("--verbose must name them:\n" + errOut.String())
	}
}
