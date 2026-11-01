package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/mod/modfile"
)

// offlineOpts is the shape every measurement test uses: a local walk, no proxy
// call, so that no test depends on the network or on what is published today.
func offlineOpts(dir, floor string, owners Owners) options {
	return options{dir: dir, floor: floor, owners: owners, jobs: 2, offline: true}
}

// runCmd drives the real command the way a user does, and returns its exit
// code with whatever it wrote. Driving the cobra command rather than the
// functions under it is what keeps the flags, the exit codes and the help in
// the measurement: a test that calls only the inner function cannot see a flag
// that was never wired up.
func runCmd(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	c := RootCmd()
	c.SetArgs(args)
	c.SetOut(&out)
	c.SetErr(&errOut)
	err := c.Execute()
	switch {
	case err == nil:
		code = 0
	default:
		var ee *exitError
		if !errors.As(err, &ee) {
			t.Fatalf("Execute returned %T (%v), want an *exitError carrying a status", err, err)
		}
		code = ee.code
	}
	return code, out.String(), errOut.String()
}

// The planted positive: the exposure this tool exists to find must be found.
// `go 1.27.1` against a floor of `1.27.2` is the live case on go-gitsafe/gitsafe
// at v0.11.1, and if this test ever passes while Judge returns ok, the tool
// reports a clean fleet for the wrong reason.
func TestJudgeFindsTheExposureItExistsToFind(t *testing.T) {
	got := Judge(Floors{Module: "m", Go: "1.27.1"}, "1.27.2")
	if got != Below {
		t.Fatalf("go 1.27.1 against floor 1.27.2 = %q, want %q", got, Below)
	}
	if !got.Exposed() {
		t.Error("Below must count as exposed, or the exit status and the table disagree")
	}
}

// The other half: the guard must let the legitimate cases through. A tool that
// flags a module already at the floor gets routed around within a day.
func TestJudgeAllowsWhatIsAlreadyAtTheFloor(t *testing.T) {
	for _, decl := range []string{"1.27.2", "1.27.3", "1.28", "1.29.0"} {
		if got := Judge(Floors{Go: decl}, "1.27.2"); got != OK {
			t.Errorf("go %s against floor 1.27.2 = %q, want %q", decl, got, OK)
		}
	}
}

func TestJudgeSeparatesAPinnedToolchainFromAnExposure(t *testing.T) {
	// Below the floor, but a toolchain line asks for a patched toolchain: with
	// GOTOOLCHAIN=auto this builds safely and with =local it ERRORS. It fails
	// closed, so it is not the same defect as Below and must not be counted
	// with it.
	got := Judge(Floors{Go: "1.27.1", Toolchain: "go1.27.2"}, "1.27.2")
	if got != Pinned {
		t.Fatalf("go 1.27.1 + toolchain go1.27.2 = %q, want %q", got, Pinned)
	}
	if got.Exposed() {
		t.Error("a floor that fails closed must not be counted as an exposure")
	}
	// And a toolchain line that is itself too low does not rescue anything.
	if got := Judge(Floors{Go: "1.27.0", Toolchain: "go1.27.1"}, "1.27.2"); got != Below {
		t.Errorf("toolchain go1.27.1 under floor 1.27.2 = %q, want %q", got, Below)
	}
}

func TestJudgeDistinguishesAbsentFromUnreadable(t *testing.T) {
	// "" and "garbage" call for different fixes, so they must not collapse.
	if got := Judge(Floors{}, "1.27.2"); got != NoFloor {
		t.Errorf("no go directive = %q, want %q", got, NoFloor)
	}
	if got := Judge(Floors{Go: "tip"}, "1.27.2"); got != Unreadable {
		t.Errorf("go tip = %q, want %q", got, Unreadable)
	}
	if !NoFloor.Exposed() {
		t.Error("a module with no floor at all is below every floor")
	}
}

// A typo in -floor must not make every module read as ok. go/version compares
// an invalid version as LESS than every valid one, so an unvalidated floor of
// "1.27.2 " or "go-1.27.2" would silently pass the whole fleet.
func TestAnUnreadableFloorIsRefusedRatherThanPassingEverything(t *testing.T) {
	for _, bad := range []string{"", "tip", "go-1.27.2", "27.2", "latest"} {
		if err := ValidateFloor(bad); err == nil {
			t.Errorf("ValidateFloor(%q) = nil, want an error", bad)
		}
	}
	for _, good := range []string{"1.27.2", "go1.27.2", "1.28"} {
		if err := ValidateFloor(good); err != nil {
			t.Errorf("ValidateFloor(%q) = %v, want nil", good, err)
		}
	}
}

func TestParseFloorsReadsARealGoMod(t *testing.T) {
	// Tabs, a comment after a directive, a quoted path, a require block that
	// contains the word "go" at the start of a line inside parentheses.
	src := []byte("module\t\"github.com/go-gitsafe/gitsafe\"\n" +
		"\n" +
		"go 1.27.1 // raise with the toolchain, not alone\n" +
		"\n" +
		"toolchain go1.27.2\n" +
		"\n" +
		"require (\n" +
		"\tgolang.org/x/sys v0.1.0\n" +
		")\n")
	f, err := ParseFloors(src)
	if err != nil {
		t.Fatal(err)
	}
	if f.Module != "github.com/go-gitsafe/gitsafe" {
		t.Errorf("Module = %q", f.Module)
	}
	if f.Go != "1.27.1" {
		t.Errorf("Go = %q, want 1.27.1 (the comment must not be read as part of it)", f.Go)
	}
	if f.Toolchain != "go1.27.2" {
		t.Errorf("Toolchain = %q", f.Toolchain)
	}
}

func TestParseFloorsOnAGoModWithNoFloor(t *testing.T) {
	f, err := ParseFloors([]byte("module example.com/old\n"))
	if err != nil {
		t.Fatal(err)
	}
	if f.Go != "" || f.Toolchain != "" {
		t.Errorf("got %+v, want empty floors", f)
	}
	if f.Module != "example.com/old" {
		t.Errorf("Module = %q", f.Module)
	}
}

// ⛔ The trap this pins: modfile.ParseLax does not fill File.Toolchain. In lax
// mode x/mod keeps only go, module, require and retract and drops the rest.
//
// That is the one directive whose loss would turn a module that FAILS CLOSED
// into a reported exposure, so this test asserts the end result — the verdict —
// and not the mechanism. A future "simplification" back to f.Toolchain alone
// fails here.
func TestTheToolchainDirectiveSurvivesTheLaxParserAndDecidesTheVerdict(t *testing.T) {
	src := []byte("module example.com/x\n\ngo 1.27.1\n\ntoolchain go1.27.2\n")

	// Whether upstream still drops it is logged, not asserted: if x/mod starts
	// filling Toolchain in lax mode the recovery becomes redundant, which is
	// not a failure.
	if lax, err := modfile.ParseLax("go.mod", src, nil); err == nil && lax.Toolchain != nil {
		t.Log("x/mod now fills Toolchain in lax mode; the syntax-tree recovery is redundant")
	}

	f, err := ParseFloors(src)
	if err != nil {
		t.Fatal(err)
	}
	if f.Toolchain != "go1.27.2" {
		t.Fatalf("Toolchain = %q, want go1.27.2 — losing it turns every pinned "+
			"module into a false alarm", f.Toolchain)
	}
	if got := Judge(f, "1.27.2"); got != Pinned {
		t.Errorf("verdict = %q, want %q: this module fails closed under "+
			"GOTOOLCHAIN=local, it does not build vulnerable code", got, Pinned)
	}
}

// A go.mod the toolchain cannot parse must come back as an ERROR, not as a
// module with no floor.
//
// This is a behaviour change from the first version of this tool, which
// hand-parsed three line prefixes: it accepted anything, so a corrupt go.mod
// read as "declares no floor" — a verdict of no-go, which is a finding. The
// difference matters because the two call for opposite actions: a module with
// no floor needs a directive added, a corrupt one needs looking at.
func TestAnUnparseableGoModIsAnErrorAndNotAFloorOfNothing(t *testing.T) {
	for _, bad := range []string{
		"module example.com/x\n\ngo\n",              // a directive with no argument
		"module example.com/x\n\nrequire (\n",       // an unclosed block
		"module\n",                                  // module with no path
		"module example.com/x\n\ngo 1.27.1 1.2.3\n", // two arguments
	} {
		if _, err := ParseFloors([]byte(bad)); err == nil {
			t.Errorf("ParseFloors(%q) = nil error, want one", bad)
		}
	}
	// The other half: a go.mod carrying a statement this version of x/mod does
	// not know is still readable, because ParseLax is used. Refusing it would
	// turn a readable module into a gap in the census.
	f, err := ParseFloors([]byte("module example.com/x\n\ngo 1.27.2\n\nwibble v1 // not a real directive\n"))
	if err != nil {
		t.Fatalf("ParseLax refused an unknown statement: %v", err)
	}
	if f.Go != "1.27.2" {
		t.Errorf("Go = %q, want 1.27.2", f.Go)
	}
}

// This is the test that the awk one-liner could not have. ReadBinaries must
// report the toolchain of a binary built HERE, with a version this test knows
// independently — so an empty answer fails instead of reading as "the binary
// does not say".
func TestReadBinariesReportsTheToolchainThatBuiltABinary(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go toolchain on PATH")
	}
	src := t.TempDir()
	write(t, filepath.Join(src, "go.mod"), "module example.com/witness\n\ngo 1.24\n")
	write(t, filepath.Join(src, "main.go"), "package main\n\nfunc main() {}\n")

	bin := t.TempDir()
	out := filepath.Join(bin, "witness")
	if runtime.GOOS == "windows" {
		out += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", out, ".")
	cmd.Dir = src
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building the witness: %v\n%s", err, b)
	}
	// A file with no build info, to prove the NotGo count is a count and not a
	// silent discard.
	write(t, filepath.Join(bin, "notes.txt"), "not a binary\n")

	walk, err := ReadBinaries(bin)
	if err != nil {
		t.Fatal(err)
	}
	if len(walk.Binaries) != 1 {
		t.Fatalf("found %d Go binaries, want 1 (%+v)", len(walk.Binaries), walk)
	}
	got := walk.Binaries[0]
	if got.Module != "example.com/witness" {
		t.Errorf("Module = %q, want example.com/witness", got.Module)
	}
	if !strings.HasPrefix(got.GoVersion, "go1.") {
		t.Errorf("GoVersion = %q, want something like go1.27.2 — an empty "+
			"answer here is the exact failure this tool replaces", got.GoVersion)
	}
	// The independent check: it must agree with the toolchain running the test.
	if got.GoVersion != runtime.Version() {
		t.Logf("built with %s, test runs under %s (fine if GOTOOLCHAIN switched)",
			got.GoVersion, runtime.Version())
	}
	if walk.NotGo != 1 {
		t.Errorf("NotGo = %d, want 1; the text file must be counted, not dropped", walk.NotGo)
	}
	if len(walk.Errors) != 0 {
		t.Errorf("Errors = %v, want none", walk.Errors)
	}
}

func TestReadBinariesRefusesToCallAnEmptyDirectoryClean(t *testing.T) {
	err := reportBinaries(io.Discard, io.Discard, t.TempDir(), "1.27.2")
	if err == nil {
		t.Fatal("an empty directory reported no error; zero stale binaries out " +
			"of zero binaries is the best value in its column and must not pass")
	}
	if !strings.Contains(err.Error(), "nothing was measured") {
		t.Errorf("error = %v, want it to say nothing was measured", err)
	}
}

func TestReportModulesRefusesAnEmptyMeasurement(t *testing.T) {
	code, _, err := reportModules(io.Discard, io.Discard, offlineOpts(t.TempDir(), "1.27.2", nil))
	if err == nil {
		t.Fatal("walking an empty tree reported success")
	}
	if code != 2 {
		t.Errorf("exit code = %d, want 2 (could not measure), not 0 (clean) nor 1 (exposed)", code)
	}
}

// An exposure must set exit status 1, distinctly from 2: a CI lane has to be
// able to tell "found a problem" from "could not look".
func TestAnExposureExitsOneAndACleanTreeExitsZero(t *testing.T) {
	below := t.TempDir()
	fakeRepo(t, below, "go-fleettools/below")
	// ⛔ The module path MATCHES the repository, as a real one does. These
	// fixtures said `example.com/below` inside `go-fleettools/below`, and once
	// Repo.Declares existed every one of them exercised "a module another
	// project publishes" instead of the exposure it was written for — a fake
	// fixture cannot witness a real module.
	write(t, filepath.Join(below, "go.mod"), "module github.com/go-fleettools/below\n\ngo 1.27.1\n")
	code, c, err := reportModules(io.Discard, io.Discard, offlineOpts(below, "1.27.2", nil))
	if err != nil {
		t.Fatal(err)
	}
	if code != 1 {
		t.Errorf("exposed tree exit = %d, want 1", code)
	}
	if c.Modules != 1 {
		t.Errorf("kept %d modules, want 1", c.Modules)
	}

	ok := t.TempDir()
	fakeRepo(t, ok, "go-fleettools/ok")
	write(t, filepath.Join(ok, "go.mod"), "module github.com/go-fleettools/ok\n\ngo 1.27.2\n")
	code, _, err = reportModules(io.Discard, io.Discard, offlineOpts(ok, "1.27.2", nil))
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Errorf("clean tree exit = %d, want 0", code)
	}

	// And the same two cases through the COMMAND, so that the exit codes the
	// flags produce are measured and not only the inner function's.
	if got, _, _ := runCmd(t, "--offline", "--floor", "1.27.2", "--dir", below); got != 1 {
		t.Errorf("command on the exposed tree exited %d, want 1", got)
	}
	if got, _, _ := runCmd(t, "--offline", "--floor", "1.27.2", "--dir", ok); got != 0 {
		t.Errorf("command on the clean tree exited %d, want 0", got)
	}
}

// The three outcomes must stay apart at the command boundary too: a lane that
// cannot tell "found an exposure" (1) from "could not measure" (2) eventually
// treats a broken scan as a clean one.
func TestTheCommandKeepsTheThreeOutcomesApart(t *testing.T) {
	root := t.TempDir()
	fakeRepo(t, root, "go-fleettools/x")
	write(t, filepath.Join(root, "go.mod"), "module github.com/go-fleettools/x\n\ngo 1.27.1\n")

	if got, _, _ := runCmd(t); got != 2 {
		t.Errorf("no arguments exited %d, want 2 (nothing to measure)", got)
	}
	if got, _, _ := runCmd(t, "--floor", "27.2", "--dir", root, "--offline"); got != 2 {
		t.Errorf("a floor with a dropped \"1.\" exited %d, want 2", got)
	}
	if got, _, _ := runCmd(t, "--floor", "1.27.2", "--dir", t.TempDir(), "--offline"); got != 2 {
		t.Errorf("an empty tree exited %d, want 2", got)
	}
	if got, _, _ := runCmd(t, "--floor", "1.27.2", "--dir", root, "--offline"); got != 1 {
		t.Errorf("an exposure exited %d, want 1", got)
	}
}

// Every flag the help promises must exist, because a flag named only in prose
// is a flag a reader will type and a shell will reject.
func TestEveryFlagTheHelpNamesIsWiredUp(t *testing.T) {
	c := RootCmd()
	for _, name := range []string{"dir", "bins", "floor", "owners", "jobs", "offline", "list", "verbose",
		"baseline", "write-baseline"} {
		if c.Flags().Lookup(name) == nil {
			t.Errorf("--%s is named in the help but not registered", name)
		}
	}
	// POSIX short forms, which the fleet's other commands all offer.
	for _, short := range []string{"d", "b", "f", "o", "j", "l", "v"} {
		if c.Flags().ShorthandLookup(short) == nil {
			t.Errorf("-%s is not registered", short)
		}
	}
}

// The incident this guard exists for: a first run of this tool walked a
// development machine, picked up the module cache and a checkout of the Go
// distribution, and reported "532 modules below the floor". Every one of those
// counts was true and the population was wrong — nobody here can raise the
// floor of k8s.io/api. A go.mod in no work tree is SET ASIDE and counted, never
// judged.
func TestAGoModInNoWorkTreeIsSetAsideRatherThanCounted(t *testing.T) {
	root := t.TempDir()
	fakeRepo(t, root, "go-fleettools/real")
	write(t, filepath.Join(root, "go.mod"), "module github.com/go-fleettools/real\n\ngo 1.27.2\n")

	// A module cache entry lives under the tree but in no work tree of its own;
	// it is only inside this one because the walk started above it. Give it its
	// own directory outside any .git by walking a SEPARATE tree.
	cache := t.TempDir()
	deep := filepath.Join(cache, "pkg", "mod", "k8s.io", "api@v0.36.0")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(deep, "go.mod"), "module k8s.io/api\n\ngo 1.26.0\n")

	code, c, err := reportModules(io.Discard, io.Discard, offlineOpts(cache, "1.27.2", nil))
	if err == nil {
		t.Fatalf("a tree of nothing but cached modules was measured: code=%d census=%+v", code, c)
	}
	if c.NotInRepo != 1 {
		t.Errorf("NotInRepo = %d, want 1 — the cached module must be counted as set aside", c.NotInRepo)
	}
	if c.Modules != 0 {
		t.Errorf("kept %d modules, want 0", c.Modules)
	}
	// The other half: the real repository IS measured.
	code, c, err = reportModules(io.Discard, io.Discard, offlineOpts(root, "1.27.2", nil))
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 || c.Modules != 1 {
		t.Errorf("the real repository: code=%d kept=%d, want 0 and 1", code, c.Modules)
	}
}

func TestOwnersNarrowsThePopulationAndCountsWhatItDropped(t *testing.T) {
	root := t.TempDir()
	mine := filepath.Join(root, "mine")
	theirs := filepath.Join(root, "theirs")
	for _, d := range []string{mine, theirs} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	fakeRepo(t, mine, "go-fleettools/mine")
	write(t, filepath.Join(mine, "go.mod"), "module github.com/go-fleettools/mine\n\ngo 1.27.1\n")
	fakeRepo(t, theirs, "someone-else/theirs")
	write(t, filepath.Join(theirs, "go.mod"), "module github.com/someone-else/theirs\n\ngo 1.20\n")

	code, c, err := reportModules(io.Discard, io.Discard, offlineOpts(root, "1.27.2", ParseOwners("go-*")))
	if err != nil {
		t.Fatal(err)
	}
	if c.Modules != 1 {
		t.Errorf("kept %d, want only the go-* one", c.Modules)
	}
	if c.OutOfScope != 1 {
		t.Errorf("OutOfScope = %d, want 1 — a dropped module must be counted, not vanish", c.OutOfScope)
	}
	// Exit 1 for the one in scope, and the other one's much older floor must
	// not be what produced it.
	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}

	// And the opposite population returns the opposite answer, which is the
	// control that -owners is doing anything at all.
	_, c, err = reportModules(io.Discard, io.Discard, offlineOpts(root, "1.27.2", ParseOwners("someone-*")))
	if err != nil {
		t.Fatal(err)
	}
	if c.Modules != 1 || c.OutOfScope != 1 {
		t.Errorf("mirrored population: kept=%d dropped=%d, want 1 and 1", c.Modules, c.OutOfScope)
	}
}

func TestSplitRemoteHandlesTheThreeSpellings(t *testing.T) {
	for _, url := range []string{
		"https://github.com/go-gitsafe/gitsafe.git",
		"https://github.com/go-gitsafe/gitsafe",
		"git@github.com:go-gitsafe/gitsafe.git",
		"ssh://git@github.com/go-gitsafe/gitsafe.git",
	} {
		host, owner, name := splitRemote(url)
		if host != "github.com" || owner != "go-gitsafe" || name != "gitsafe" {
			t.Errorf("splitRemote(%q) = %q %q %q", url, host, owner, name)
		}
	}
}

// A remote URL carrying a credential is the exact shape that has leaked a token
// here twice. This tool only reads, but it PRINTS what it reads, so the
// credential must be dropped before it can reach a report.
func TestSplitRemoteDropsACredentialRatherThanReportingIt(t *testing.T) {
	const secret = "ghp0000000000000000000000000000000000"
	url := "https://x-access-token:" + secret + "@github.com/go-gitsafe/gitsafe.git"
	host, owner, name := splitRemote(url)
	if host != "github.com" || owner != "go-gitsafe" || name != "gitsafe" {
		t.Fatalf("splitRemote(credentialed) = %q %q %q", host, owner, name)
	}
	for _, got := range []string{host, owner, name} {
		if strings.Contains(got, secret) || strings.Contains(got, "x-access-token") {
			t.Errorf("a credential survived into %q", got)
		}
	}
}

func TestFindGitHandlesALinkedWorktree(t *testing.T) {
	// Parallel sessions each work in their own linked worktree, so this is the
	// normal shape here, not an edge case: .git is a FILE naming a gitdir under
	// the common directory, and config lives in the common directory.
	common := t.TempDir()
	fakeRepo(t, common, "go-fleettools/fleettools")
	wt := t.TempDir()
	gitdir := filepath.Join(common, ".git", "worktrees", "branch-x")
	if err := os.MkdirAll(gitdir, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(wt, ".git"), "gitdir: "+gitdir+"\n")

	repo, err := newRepoFinder().Of(wt)
	if err != nil {
		t.Fatal(err)
	}
	if repo.Slug() != "go-fleettools/fleettools" {
		t.Errorf("worktree resolved to %q, want go-fleettools/fleettools", repo.Slug())
	}
}

// fakeRepo gives a directory the minimum that makes it a work tree with an
// origin: a .git directory holding a config.
func fakeRepo(t *testing.T, root, slug string) {
	t.Helper()
	g := filepath.Join(root, ".git")
	if err := os.MkdirAll(g, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(g, "config"),
		"[core]\n\tbare = false\n[remote \"origin\"]\n"+
			"\turl = https://github.com/"+slug+".git\n"+
			"\tfetch = +refs/heads/*:refs/remotes/origin/*\n")
}

func TestFindGoModsSkipsTestdataAndVendor(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "go.mod"), "module github.com/go-fleettools/real\n\ngo 1.27.2\n")
	for _, skip := range []string{"testdata", "vendor", ".git"} {
		d := filepath.Join(root, skip, "inner")
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		// A corpus routinely carries deliberately broken go.mod files; counting
		// those as fleet modules puts fabricated defects in the report.
		write(t, filepath.Join(d, "go.mod"), "module example.com/"+skip+"\n\ngo 1.0\n")
	}
	found, problems, err := FindGoMods(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 0 {
		t.Errorf("problems = %v", problems)
	}
	if len(found) != 1 {
		t.Fatalf("found %v, want only the real one", found)
	}
}

// A module path comes from a `module` directive in a go.mod found by walking a
// disk: untrusted input that this tool concatenates into a URL. The guard is
// tested in both directions, because one that rejects real module paths would
// be removed within a day and one that accepts a traversal is not a guard.
func TestAModulePathFromDiskCannotCarryATraversalIntoAURL(t *testing.T) {
	for _, bad := range []string{
		"", "/etc/passwd", "../../../etc/passwd",
		"github.com/../../x", "github.com//x", "github.com/x/",
		"github.com/x?y=z", "github.com/x#y", "github.com/x y",
		"https://github.com/x/y", "github.com/x\ny",
		"github.com/x%2e%2e/y", "github.com/x@y",
	} {
		if ValidModulePath(bad) {
			t.Errorf("ValidModulePath(%q) = true, want false", bad)
		}
	}
	for _, good := range []string{
		"github.com/go-fleettools/fleettools",
		"github.com/go-fleettools/fleettools/cmd/modfloor",
		"golang.org/x/mod",
		"k8s.io/api",
		"gopkg.in/yaml.v3",
		"github.com/BurntSushi/toml",
		"example.com/m/v2",
	} {
		if !ValidModulePath(good) {
			t.Errorf("ValidModulePath(%q) = false, want true — a guard that "+
				"rejects real module paths gets removed, not fixed", good)
		}
	}

	// And the refusal must reach the caller rather than being swallowed into
	// an empty answer.
	p := NewProxy()
	if _, err := p.Latest(t.Context(), "../../../etc/passwd"); !errors.Is(err, ErrBadModulePath) {
		t.Errorf("Latest on a traversal returned %v, want ErrBadModulePath", err)
	}
	if _, err := p.Mod(t.Context(), "github.com/x/y", "../../etc"); !errors.Is(err, ErrBadModulePath) {
		t.Errorf("Mod with a bad version returned %v, want ErrBadModulePath", err)
	}
}

// A truncated go.mod parses perfectly well and simply appears to declare no
// floor, which would read as a finding rather than as a failure to read. So an
// oversized file is REFUSED, not truncated.
func TestAnOversizedGoModIsRefusedRatherThanTruncated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "go.mod")
	big := make([]byte, maxTextFile+1)
	for i := range big {
		big[i] = 'x'
	}
	write(t, path, "module example.com/big\n\ngo 1.27.2\n"+string(big))

	if _, err := readCapped(path); err == nil {
		t.Fatal("an oversized go.mod was read; a truncated one would appear to declare no floor")
	}
	// The other half: a normal file is read whole, to the last byte.
	small := filepath.Join(dir, "small.mod")
	const body = "module example.com/small\n\ngo 1.27.2\n"
	write(t, small, body)
	got, err := readCapped(small)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Errorf("readCapped returned %q, want the whole file", got)
	}
}

func TestEscapePathAppliesTheProxyCaseEncoding(t *testing.T) {
	for in, want := range map[string]string{
		"github.com/go-gitsafe/gitsafe": "github.com/go-gitsafe/gitsafe",
		"github.com/BurntSushi/toml":    "github.com/!burnt!sushi/toml",
	} {
		got, err := escapePath(in)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("escapePath(%q) = %q, want %q", in, got, want)
		}
	}
}

// write plants a file, creating its parent directories: a fixture for this tool
// is often a go.mod in a SUBDIRECTORY of a repository (a monorepo module, a
// vendored tree), and a helper that could not make one turned three tests into
// "no such file or directory" instead of the measurement they were written for.
func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
