package main

import (
	"go/build/constraint"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// ⛔ THE INCIDENT (#19). Measured 2026-11-15 over this fleet: 351 of 843 rows
// were `*.github.io`, and ALL SEVEN rows reading "fixed locally, UNRELEASED" —
// the single most actionable line this report prints — were Hugo landing sites
// whose go.mod exists only to pin a theme. The verdict was CORRECT on its own
// terms, which is why it would have been re-reported forever and taught its
// reader to skim the column.
func TestALandingSiteIsSetAsideAndALibraryBesideItIsNot(t *testing.T) {
	root := t.TempDir()

	landing := filepath.Join(root, "landing")
	fakeRepo(t, landing, "go-tex/go-tex.github.io")
	write(t, filepath.Join(landing, "go.mod"),
		"module github.com/go-tex/go-tex.github.io\n\ngo 1.27.1\n\nrequire github.com/go-fleettools/landingkit v0.29.0\n")
	// What a landing site actually holds. None of it is Go.
	write(t, filepath.Join(landing, "hugo.toml"), "baseURL = 'https://go-tex.github.io/'\n")
	write(t, filepath.Join(landing, "content", "_index.md"), "# go-tex\n")

	lib := filepath.Join(root, "lib")
	fakeRepo(t, lib, "go-tex/engine")
	write(t, filepath.Join(lib, "go.mod"), "module github.com/go-tex/engine\n\ngo 1.27.1\n")
	hasGoCode(t, lib)

	var out, errOut strings.Builder
	o := offlineOpts(root, "1.27.3", ParseOwners("go-tex"))
	o.verbose = true
	code, c, err := reportModules(&out, &errOut, o)
	if err != nil {
		t.Fatal(err)
	}
	if code != 1 {
		t.Fatalf("the library is below the floor: want exit 1, got %d\n%s", code, out.String())
	}
	if c.Modules != 1 || c.NoPackages != 1 {
		t.Fatalf("kept %d and set aside %d with no package, want 1 and 1\n%s",
			c.Modules, c.NoPackages, out.String())
	}
	// ⛔ SET ASIDE IS NOT DROPPED: a population is only honest when what it put
	// aside is printed beside what it kept.
	if !strings.Contains(out.String(), "1 with no buildable Go package") {
		t.Error("the population line must say what it set aside:\n" + out.String())
	}
	if strings.Contains(out.String(), "go-tex.github.io") {
		t.Error("a module nobody installs reached the verdict table:\n" + out.String())
	}
	if !strings.Contains(errOut.String(), "no packages:") {
		t.Error("--verbose must name it:\n" + errOut.String())
	}
}

// ⛔ THE TEST IS THE PACKAGES, NEVER THE NAME. go-pkgx/go-pkgx.github.io ships
// tools/playground, tools/versioncheck, tools/vulnmods and tools/wasmstage as
// modules in this same sweep: a rule keyed on `*.github.io` would exempt real
// Go code, which is a false clean rather than noise.
func TestARepositoryNamedForAPagesSiteThatShipsGoCodeIsStillJudged(t *testing.T) {
	root := t.TempDir()
	fakeRepo(t, root, "go-pkgx/go-pkgx.github.io")
	write(t, filepath.Join(root, "go.mod"), "module github.com/go-pkgx/go-pkgx.github.io\n\ngo 1.27.1\n")
	write(t, filepath.Join(root, "hugo.toml"), "baseURL = 'https://go-pkgx.github.io/'\n")
	// The site's own module has no Go at its root, but a real package below it.
	hasGoCode(t, filepath.Join(root, "tools", "playground"))

	code, c, err := reportModules(io.Discard, io.Discard, offlineOpts(root, "1.27.3", ParseOwners("go-pkgx")))
	if err != nil {
		t.Fatal(err)
	}
	if c.Modules != 1 || c.NoPackages != 0 {
		t.Fatalf("kept %d, set aside %d: a module with Go code anywhere in its own tree must be judged",
			c.Modules, c.NoPackages)
	}
	if code != 1 {
		t.Errorf("exit %d, want 1: it declares 1.27.1 under floor 1.27.3", code)
	}
}

func TestWhatCountsAsAPackage(t *testing.T) {
	for _, c := range []struct {
		name string
		file string
		src  string
		want bool
	}{
		// ⛔ "Some configuration" is the whole point. A module whose only source
		// builds on one system is installable on that system.
		{"a constraint naming one system", "only.go", "//go:build windows\n\npackage x\n", true},
		{"a negated constraint", "most.go", "//go:build !windows\n\npackage x\n", true},
		{"a filename suffix", "x_windows.go", "package x\n", true},
		{"an architecture", "x_arm64.go", "package x\n", true},
		{"a system and an architecture", "both.go", "//go:build linux && arm64\n\npackage x\n", true},
		{"a release tag", "new.go", "//go:build go1.27\n\npackage x\n", true},
		{"cgo", "c.go", "//go:build cgo\n\npackage x\n", true},
		{"a test file", "x_test.go", "package x\n", true},

		// ⛔ The idiom a repository uses to say "this is not part of the build".
		{"a tags-only file", "tools.go", "//go:build tools\n\npackage tools\n", false},
		{"an ignored file", "gen.go", "//go:build ignore\n\npackage main\n", false},
		{"the old plus-build syntax", "old.go", "// +build tools\n\npackage tools\n", false},
		{"a custom tag with a system", "mix.go", "//go:build tools && linux\n\npackage x\n", false},

		// ⛔ And a custom tag ORed with a system is reachable without -tags.
		{"a custom tag or a system", "or.go", "//go:build tools || linux\n\npackage x\n", true},

		// A constraint the toolchain itself would reject is a defect in that
		// file, not an absence of code: the module stays judged.
		{"an unparseable constraint", "bad.go", "//go:build &&\n\npackage x\n", true},

		// The go tool ignores these outright, so neither is code.
		{"a file the go tool ignores", "_scratch.go", "package x\n", false},
		{"a dotfile", ".hidden.go", "package x\n", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			write(t, filepath.Join(dir, c.file), c.src)
			got, err := HasGoPackages(dir)
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Errorf("HasGoPackages with %s = %v, want %v", c.file, got, c.want)
			}
		})
	}
}

// ⛔ EVAL UNDER ONE ASSIGNMENT IS NOT SATISFIABILITY, and that is the mistake
// that would have exempted real code: `!windows` reads FALSE the moment
// "windows" is answered true, yet it builds on every other system. This test
// drives the function the walk uses, so a rewrite back to a single Eval goes
// red here and not in a field report.
func TestANegatedSystemIsSatisfiableEvenThoughOneEvaluationSaysOtherwise(t *testing.T) {
	e, err := constraint.Parse("//go:build !windows")
	if err != nil {
		t.Fatal(err)
	}
	if e.Eval(func(tag string) bool { return standardTag(tag) }) {
		t.Fatal("the premise of this test is gone: a single Eval with every standard tag true used to say false here")
	}
	if !satisfiable(e) {
		t.Error("!windows builds on linux, so the constraint is satisfiable")
	}
}

// ⛔ A GOOS MISSING FROM standardTag IS THE DANGEROUS ERROR: a module whose
// only source is constrained to that system would look unbuildable and leave
// the population SILENTLY. The toolchain is the authority for that list, not
// this file, so the list is checked against it.
func TestEveryPlatformTheToolchainSupportsIsAStandardTag(t *testing.T) {
	out, err := exec.Command("go", "tool", "dist", "list").Output()
	if err != nil {
		t.Skip("no go toolchain to ask:", err)
	}
	pairs := 0
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		goos, goarch, ok := strings.Cut(line, "/")
		if !ok {
			t.Fatalf("go tool dist list printed %q, which is not GOOS/GOARCH", line)
		}
		pairs++
		if !standardTag(goos) {
			t.Errorf("GOOS %q is not in knownOS: a module constrained to it would be set aside", goos)
		}
		if !standardTag(goarch) {
			t.Errorf("GOARCH %q is not in knownArch: a module constrained to it would be set aside", goarch)
		}
	}
	// ⛔ A measurement that read nothing must not pass. An empty list would
	// satisfy every assertion above it.
	if pairs < 20 {
		t.Fatalf("go tool dist list printed %d pairs; that is not a platform list", pairs)
	}
}

// ⛔ EVERY PARSER GETS A FUZZER. constraintOf reads the top of files this walk
// did not write, and satisfiable then runs a search over what it returns: a
// panic there stops a fleet sweep, and a non-terminating search stops it
// without even saying so. The seeds are the corpus; a crash would be committed
// under testdata/fuzz as Go writes it.
func FuzzConstraintOf(f *testing.F) {
	for _, seed := range []string{
		"//go:build linux\n\npackage x\n",
		"//go:build !windows && (amd64 || arm64)\n\npackage x\n",
		"//go:build tools\n\npackage x\n",
		"// +build linux\n// +build amd64\n\npackage x\n",
		"//go:build\n",
		"//go:build &&\n",
		"//go:build ((((((((\n",
		"//go:build a && b && c && d && e && f && g && h && i && j\n",
		"//go:build linux\npackage x\n//go:build windows\n",
		"package x\n//go:build linux\n",
		"",
		"\n\n\n",
		"//go:build \u0000\n",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, src string) {
		e, ok := constraintOf([]byte(src))
		if !ok {
			return
		}
		if e == nil {
			t.Fatal("constraintOf said it found a constraint and returned nil")
		}
		// Must terminate, must not panic, and must answer the same way twice:
		// the search is over a map's keys, and a verdict that depends on
		// iteration order is a verdict that changes between runs.
		if satisfiable(e) != satisfiable(e) {
			t.Fatalf("satisfiable is not deterministic for %q", src)
		}
	})
}

// ⛔ A custom tag must NOT be standard, or the whole check collapses into "has
// a .go file" — which is the behaviour this change exists to replace.
func TestACustomTagIsNotStandard(t *testing.T) {
	for _, tag := range []string{"tools", "ignore", "integration", "e2e", "wasmstage", "windowsxp"} {
		if standardTag(tag) {
			t.Errorf("%q must not be a standard tag", tag)
		}
	}
}

// A nested module has its own go.mod and therefore its own floor; its code is
// not evidence about the module above it.
func TestANestedModulesCodeDoesNotSpeakForTheModuleAboveIt(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "go.mod"), "module example.com/outer\n\ngo 1.27.1\n")
	inner := filepath.Join(root, "inner")
	write(t, filepath.Join(inner, "go.mod"), "module example.com/outer/inner\n\ngo 1.27.1\n")
	hasGoCode(t, inner)

	got, err := HasGoPackages(root)
	if err != nil {
		t.Fatal(err)
	}
	if got {
		t.Error("the inner module's code was counted for the outer one")
	}
	// The control: the same file one directory up IS the outer module's code,
	// so the nested go.mod is what decided it.
	hasGoCode(t, root)
	if got, err = HasGoPackages(root); err != nil || !got {
		t.Errorf("HasGoPackages = %v, %v — the control must find the outer module's own file", got, err)
	}
}

// testdata carries deliberately broken sources; a directory starting with "_"
// or "." is one the go tool never builds. Counting either would make a corpus
// look like a library.
func TestADirectoryTheGoToolNeverBuildsIsNotCode(t *testing.T) {
	for _, dir := range []string{"testdata", "_scratch", ".cache", "node_modules", "vendor"} {
		t.Run(dir, func(t *testing.T) {
			root := t.TempDir()
			write(t, filepath.Join(root, "go.mod"), "module example.com/x\n\ngo 1.27.1\n")
			hasGoCode(t, filepath.Join(root, dir, "pkg"))
			got, err := HasGoPackages(root)
			if err != nil {
				t.Fatal(err)
			}
			if got {
				t.Errorf("a .go file under %s/ was counted as a package", dir)
			}
		})
	}
}

// ⛔ A CATEGORY THAT IS COUNTED AND NOT PRINTED IS A MODULE THAT VANISHED. The
// NoPackages field was added in this change, and nothing would have gone red
// had its count never reached the population line: a set-aside module would
// simply have been missing from a report whose numbers all still added up.
//
// So this walks the Census by reflection: every field but the three named below
// must have a witness in one tree AND a phrase on the printed line. Adding a
// field to Census turns this red until both exist.
func TestEverySetAsideCategoryIsWitnessedAndPrinted(t *testing.T) {
	// field name -> the words the population line must use for it
	phrases := map[string]string{
		"OutOfScope": "out of scope",
		"NotInRepo":  "in no work tree",
		"Foreign":    "published by another project",
		"NoPackages": "with no buildable Go package",
		"NoModule":   "without a module directive",
	}
	// GoMods and Modules are the two totals, not set-aside kinds. Unreadable
	// needs a directory the filesystem may refuse to make unreadable, so it has
	// its own test above, which skips rather than lying.
	notACategory := map[string]bool{"GoMods": true, "Modules": true, "Unreadable": true}

	root := t.TempDir()

	// Kept: our repository, our module, with code.
	mine := filepath.Join(root, "mine")
	fakeRepo(t, mine, "go-fleettools/mine")
	write(t, filepath.Join(mine, "go.mod"), "module github.com/go-fleettools/mine\n\ngo 1.27.1\n")
	hasGoCode(t, mine)
	// Foreign: another project's module carried inside ours.
	write(t, filepath.Join(mine, "srcs", "yaml", "go.mod"), "module sigs.k8s.io/yaml\n\ngo 1.22\n")
	hasGoCode(t, filepath.Join(mine, "srcs", "yaml"))
	// NoPackages: a landing site.
	landing := filepath.Join(root, "landing")
	fakeRepo(t, landing, "go-fleettools/go-fleettools.github.io")
	write(t, filepath.Join(landing, "go.mod"),
		"module github.com/go-fleettools/go-fleettools.github.io\n\ngo 1.27.1\n")
	// OutOfScope: another owner.
	theirs := filepath.Join(root, "theirs")
	fakeRepo(t, theirs, "someone-else/theirs")
	write(t, filepath.Join(theirs, "go.mod"), "module github.com/someone-else/theirs\n\ngo 1.20\n")
	hasGoCode(t, theirs)
	// NotInRepo: a go.mod under no work tree at all.
	loose := filepath.Join(root, "loose")
	write(t, filepath.Join(loose, "go.mod"), "module example.com/loose\n\ngo 1.20\n")
	hasGoCode(t, loose)
	// NoModule: a go.mod that declares none.
	write(t, filepath.Join(root, "nomod", "go.mod"), "go 1.20\n")

	var out strings.Builder
	code, c, err := reportModules(&out, io.Discard, offlineOpts(root, "1.27.3", ParseOwners("go-fleettools")))
	if err != nil {
		t.Fatal(err)
	}
	if code != 1 || c.Modules != 1 {
		t.Fatalf("code=%d kept=%d, want 1 and 1 (only github.com/go-fleettools/mine)\n%s",
			code, c.Modules, out.String())
	}

	v := reflect.ValueOf(c)
	for i := 0; i < v.NumField(); i++ {
		name := v.Type().Field(i).Name
		if notACategory[name] {
			continue
		}
		phrase, ok := phrases[name]
		if !ok {
			t.Errorf("Census.%s is a new set-aside category: give it a witness in this tree "+
				"and the words it is printed with on the population line", name)
			continue
		}
		if got := v.Field(i).Int(); got == 0 {
			t.Errorf("Census.%s = 0: this tree no longer witnesses that category, so nothing "+
				"here proves it is counted", name)
		}
		if !strings.Contains(out.String(), phrase) {
			t.Errorf("the population line never says %q, so Census.%s is counted and not printed:\n%s",
				phrase, name, out.String())
		}
	}
}

// ⛔ AN UNREADABLE DIRECTORY IS NOT AN ABSENCE OF CODE. That is the one wrong
// answer that would remove a module from the population without saying so, and
// a scan which cannot read must report that it could not read.
func TestAnUnreadableDirectoryIsCountedUnreadableAndNotAsAMissingPackage(t *testing.T) {
	root := t.TempDir()
	fakeRepo(t, root, "go-fleettools/shut")
	write(t, filepath.Join(root, "go.mod"), "module github.com/go-fleettools/shut\n\ngo 1.27.1\n")
	shut := filepath.Join(root, "pkg")
	hasGoCode(t, shut)
	if err := os.Chmod(shut, 0o000); err != nil {
		t.Skip("cannot make a directory unreadable here:", err)
	}
	t.Cleanup(func() { _ = os.Chmod(shut, 0o755) })
	if _, err := os.ReadDir(shut); err == nil {
		t.Skip("this user reads a 0000 directory anyway, so there is nothing to witness")
	}

	code, c, err := reportModules(io.Discard, io.Discard, offlineOpts(root, "1.27.3", ParseOwners("go-fleettools")))
	if c.NoPackages != 0 {
		t.Errorf("NoPackages = %d: a directory that could not be read was read as having no code", c.NoPackages)
	}
	// It is counted twice — once by the go.mod walk and once by the package
	// check, both of which looked and neither of which could read — and the
	// census field counts FINDINGS, not directories. What this test is about is
	// that the number is not zero: the module must not disappear quietly.
	if c.Unreadable == 0 {
		t.Errorf("Unreadable = 0: the directory nobody could read left no trace (code=%d err=%v)", code, err)
	}
	if err == nil {
		t.Error("a run that measured no module must be an error, not a clean sweep")
	}
}
