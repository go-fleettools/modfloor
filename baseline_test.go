package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// The round trip, and the one thing it must carry: the floor and the mode. A
// set of module names without them is a set of names about nothing.
func TestABaselineRecordsTheFloorAndModeItWasTakenAgainst(t *testing.T) {
	rows := []Row{
		{Module: "example.com/a", Verdict: Below},
		{Module: "example.com/ok", Verdict: OK},
		{Module: "example.com/pinned", Verdict: Pinned},
		{Module: "example.com/b", Verdict: NoFloor},
	}
	var w strings.Builder
	if err := WriteBaseline(&w, rows, "1.27.3", false); err != nil {
		t.Fatal(err)
	}
	b, err := ReadBaseline(strings.NewReader(w.String()))
	if err != nil {
		t.Fatal(err)
	}
	if b.Floor != "1.27.3" || b.Mode != "published" {
		t.Fatalf("floor %q mode %q", b.Floor, b.Mode)
	}
	// ⛔ EVERY measured module is recorded, the clean ones included, and they
	// are the point: without them a module that WAS ok and is exposed now
	// cannot be told from one that was never measured.
	if len(b.Seen) != 4 {
		t.Fatalf("recorded %d modules, want all 4: %v", len(b.Seen), b.Seen)
	}
	if b.ExposedCount() != 2 || !b.Exposed("example.com/a") || !b.Exposed("example.com/b") {
		t.Fatalf("exposed = %d: %v", b.ExposedCount(), b.Seen)
	}
	// `ok` is not an exposure, and neither is `toolchain`: it fails CLOSED
	// under GOTOOLCHAIN=local rather than building vulnerable code. Both are
	// recorded as SEEN and neither counts as exposed.
	if b.Exposed("example.com/ok") || b.Exposed("example.com/pinned") {
		t.Error("a clean module was recorded as an exposure")
	}
	if _, seen := b.Seen["example.com/pinned"]; !seen {
		t.Error("a toolchain-pinned module must still be recorded as measured")
	}
}

// ⛔ The refusals. Each of these would otherwise be a gate that passes for the
// wrong reason, which is worse than no gate.
func TestABaselineThatAnswersADifferentQuestionIsRefused(t *testing.T) {
	for _, c := range []struct {
		name, src, want string
	}{
		{"no floor", "mode published\nBELOW\tx\n", "floor"},
		{"no mode", "floor 1.27.3\nBELOW\tx\n", "mode"},
		{"unknown mode", "floor 1.27.3\nmode guessed\n", "published"},
		{"unknown header", "floor 1.27.3\nmode published\nceiling 2\n", "unknown header"},
		{"verdict with no module", "floor 1.27.3\nmode published\nBELOW\t\n", "no module path"},
		{"neither header nor cell", "floor 1.27.3\nmode published\nnonsense\n", "neither"},
	} {
		if _, err := ReadBaseline(strings.NewReader(c.src)); err == nil {
			t.Errorf("%s: accepted", c.name)
		} else if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v does not mention %q", c.name, err, c.want)
		}
	}

	// A baseline with a valid header and no rows is legitimate: it says
	// "nothing was measured as exposed", which is different from an unread
	// file. The headers are what witness that the file was read at all.
	b, err := ReadBaseline(strings.NewReader(
		"# a clean fleet\nfloor 1.27.3\nmode offline\nrecords all\ntaken 2026-11-01\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Seen) != 0 || b.ExposedCount() != 0 || b.Mode != "offline" {
		t.Fatalf("%+v", b)
	}

	// ⛔ And a baseline of the EXPOSED SET ALONE — every file modfloor ≤ v0.6.1
	// wrote — is refused rather than read as best it can be. It holds no clean
	// modules, so a real regression would be filed as population growth and
	// the gate would pass. The dangerous direction is the silent one.
	old := "floor 1.27.3\nmode published\ntaken 2026-11-01\nBELOW\texample.com/a\n"
	if _, err := ReadBaseline(strings.NewReader(old)); err == nil {
		t.Error("an exposed-only baseline was accepted")
	} else if !strings.Contains(err.Error(), "records all") ||
		!strings.Contains(err.Error(), "population growth") {
		t.Errorf("the refusal must say what it cannot tell apart: %v", err)
	}
}

// ⛔ The baseline is read back as a GATE, so a module path carrying the file's
// own separators could plant an extra line and make an unrecorded module read
// as already known — an exposure that then passes unseen. The planted positive
// is a path with a newline in it.
func TestAModulePathCannotPlantALineInTheBaseline(t *testing.T) {
	var w strings.Builder
	err := WriteBaseline(&w, []Row{
		{Module: "example.com/honest", Verdict: Below},
		{Module: "example.com/evil\nBELOW\texample.com/hidden", Verdict: Below},
	}, "1.27.3", false)
	if err == nil {
		t.Fatal("a module path with a newline was written into the baseline")
	}
	if !strings.Contains(err.Error(), "separators") {
		t.Errorf("the refusal must say why: %v", err)
	}
	// And it refuses rather than writing a partial file: nothing of the
	// honest module either, because half a baseline is a baseline that is
	// missing modules.
	if strings.Contains(w.String(), "example.com/honest") {
		t.Error("a refused write left a partial baseline behind:\n" + w.String())
	}
}

// ⛔⛔ THE ONE A SUBTRACTION GETS WRONG. A module that is no longer measured --
// its clone deleted, renamed, moved out of the owner globs, or simply not
// walked -- leaves the exposed set exactly as a repaired module does. Counting
// them together is how a campaign declares victory by losing track of its
// subjects.
func TestAModuleThatLeftThePopulationIsNotAFix(t *testing.T) {
	b := &Baseline{Floor: "1.27.3", Mode: "published", Seen: map[string]string{
		"example.com/still":    string(Below),
		"example.com/fixed":    string(Below),
		"example.com/vanished": string(Below),
		// Measured and CLEAN. This is the one that makes a regression
		// distinguishable from a clone arriving.
		"example.com/wasclean": string(OK),
	}}
	rows := []Row{
		{Module: "example.com/still", Verdict: Below},
		{Module: "example.com/fixed", Verdict: OK},
		{Module: "example.com/wasclean", Verdict: Below},
		{Module: "example.com/arrived", Verdict: Below},
		// example.com/vanished is absent from the run entirely.
	}
	rt := Compare(b, rows)
	// ⛔ Only the module that was MEASURED AND CLEAN counts as worse.
	if len(rt.Regressed) != 1 || rt.Regressed[0] != "example.com/wasclean" {
		t.Errorf("regressed = %v, want [example.com/wasclean]", rt.Regressed)
	}
	// ⛔ And the one the baseline never looked at is NOT a regression: the
	// first version of this called eight freshly cloned repositories
	// regressions 80 minutes after its own baseline.
	if len(rt.Entered) != 1 || rt.Entered[0] != "example.com/arrived" {
		t.Errorf("entered = %v, want [example.com/arrived]", rt.Entered)
	}
	if len(rt.Fixed) != 1 || rt.Fixed[0] != "example.com/fixed" {
		t.Errorf("fixed = %v, want [example.com/fixed]", rt.Fixed)
	}
	if len(rt.Gone) != 1 || rt.Gone[0] != "example.com/vanished" {
		t.Errorf("gone = %v, want [example.com/vanished]", rt.Gone)
	}

	var out strings.Builder
	if code := printRatchet(&out, b, rt, 3); code != 1 {
		t.Errorf("a module that was clean and is exposed must exit 1, got %d", code)
	}
	for _, want := range []string{
		"WORSE example.com/wasclean",
		"new to the population (not a regression) example.com/arrived",
		"NOT MEASURED THIS TIME (not a fix) example.com/vanished",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the report must keep these apart; missing %q:\n%s", want, out.String())
		}
	}
}

// ⛔⛔ AND A POPULATION THAT GREW IS NOT A FLEET THAT GOT WORSE. This fired for
// real on 2026-11-01: eight "regressions" 80 minutes after the baseline, every
// one a repository another session had just cloned (`go-compressions/xz`
// appeared on disk one minute before the run), the population growing from 348
// modules to 360. A gate that cries wolf is a gate nobody reads.
func TestPopulationGrowthDoesNotFailTheGate(t *testing.T) {
	b := &Baseline{Floor: "1.27.3", Mode: "published", Taken: "2026-11-01",
		Seen: map[string]string{"example.com/a": string(Below)}}
	rows := []Row{
		{Module: "example.com/a", Verdict: Below},
		{Module: "example.com/cloned1", Verdict: Below},
		{Module: "example.com/cloned2", Verdict: NoFloor},
	}
	rt := Compare(b, rows)
	var out strings.Builder
	if code := printRatchet(&out, b, rt, 3); code != 0 {
		t.Fatalf("clones arriving must not fail the gate, got %d\n%s", code, out.String())
	}
	if len(rt.Entered) != 2 || len(rt.Regressed) != 0 {
		t.Fatalf("entered=%v regressed=%v", rt.Entered, rt.Regressed)
	}
	// It must still be loud: they are work, and the gate says why it cannot
	// judge them.
	if !strings.Contains(out.String(), "2 module(s) entered the population exposed") {
		t.Error("entering exposed must be reported, not swallowed:\n" + out.String())
	}
}

// The gate's whole point: 262 modules still exposed is status 0, as long as it
// is the SAME 262. A lane that is red for ever is read as broken and ignored.
func TestAnUnchangedExposureIsNotAFailureButIsStillSaid(t *testing.T) {
	b := &Baseline{Floor: "1.27.3", Mode: "published", Taken: "2026-11-01",
		Seen: map[string]string{"example.com/a": string(Below), "example.com/b": string(Below)}}
	rows := []Row{{Module: "example.com/a", Verdict: Below}, {Module: "example.com/b", Verdict: Below}}
	var out strings.Builder
	if code := printRatchet(&out, b, Compare(b, rows), 2); code != 0 {
		t.Fatalf("an unchanged exposure must be status 0, got %d", code)
	}
	if !strings.Contains(out.String(), "2 still exposed, 0 WORSE") {
		t.Error("a green ratchet must still print the size of the campaign:\n" + out.String())
	}
}

// ⛔ The floor moved three times in four days, so a baseline taken against one
// floor says nothing about another. Believing it would be the exact failure
// this tool exists to catch, one level up.
func TestABaselineFromAnotherFloorOrModeIsRefused(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "floor.txt")
	var w strings.Builder
	if err := WriteBaseline(&w, []Row{{Module: "example.com/a", Verdict: Below}}, "1.27.2", false); err != nil {
		t.Fatal(err)
	}
	write(t, path, w.String())

	if _, err := loadBaseline(path, "1.27.3", false); err == nil {
		t.Error("a baseline taken at 1.27.2 judged a run at 1.27.3")
	} else if !strings.Contains(err.Error(), "1.27.2") || !strings.Contains(err.Error(), "1.27.3") {
		t.Errorf("the refusal must name both floors: %v", err)
	}
	if _, err := loadBaseline(path, "1.27.2", true); err == nil {
		t.Error("a published baseline judged an --offline run")
	}
	if _, err := loadBaseline(path, "1.27.2", false); err != nil {
		t.Errorf("the matching case must load: %v", err)
	}
	if _, err := loadBaseline(filepath.Join(dir, "absent.txt"), "1.27.2", false); err == nil {
		t.Error("a missing baseline is not an empty one")
	}
}

// ⛔ Recording the answer and then judging it against itself always passes.
// That is not a gate, so the combination is refused before anything is read.
func TestTheFlagCombinationsAGateCannotAnswerAreRefused(t *testing.T) {
	for _, c := range []struct {
		name string
		o    options
		want string
	}{
		{"both", options{dir: "x", floor: "1.27.3", baseline: "a", writeBaseline: "b"}, "always passes"},
		{"no floor", options{dir: "x", baseline: "a"}, "--floor"},
		{"nothing to measure", options{floor: "1.27.3", baseline: "a"}, "--dir"},
	} {
		if err := checkBaseline(c.o); err == nil {
			t.Errorf("%s: accepted", c.name)
		} else if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v does not mention %q", c.name, err, c.want)
		}
	}
	if err := checkBaseline(options{dir: "x", floor: "1.27.3", baseline: "a"}); err != nil {
		t.Errorf("the usable combination was refused: %v", err)
	}
	if err := checkBaseline(options{modules: []string{"example.com/a"}, floor: "1.27.3", writeBaseline: "a"}); err != nil {
		t.Errorf("module paths are enough to be measured: %v", err)
	}
}

// End to end, through the real command, because a ratchet that works in its own
// functions and not behind the flags is a ratchet nobody can run. The planted
// regression is what proves the gate can fire at all.
func TestTheRatchetPassesTheOldExposureAndFiresOnANewOne(t *testing.T) {
	root := t.TempDir()
	fakeRepo(t, filepath.Join(root, "old"), "go-fleettools/old")
	write(t, filepath.Join(root, "old", "go.mod"), "module github.com/go-fleettools/old\n\ngo 1.27.1\n")
	// Measured and CLEAN at recording time. Without a clean module in the
	// baseline there is nothing a regression can be told apart from.
	fakeRepo(t, filepath.Join(root, "clean"), "go-fleettools/clean")
	write(t, filepath.Join(root, "clean", "go.mod"), "module github.com/go-fleettools/clean\n\ngo 1.27.3\n")
	path := filepath.Join(t.TempDir(), "floor.txt")

	// Recording finds an exposure and still exits 0: it states where the
	// campaign starts, it does not judge it.
	code, out, _ := runCmd(t, "--dir", root, "--owners", "go-fleettools", "--offline",
		"--floor", "1.27.3", "--write-baseline", path)
	if code != 0 {
		t.Fatalf("writing a baseline must exit 0, got %d\n%s", code, out)
	}
	if !strings.Contains(out, "wrote 1 exposed module(s)") {
		t.Fatal(out)
	}

	// The same tree, judged against what was just recorded: still exposed,
	// nothing worse, status 0.
	code, out, _ = runCmd(t, "--dir", root, "--owners", "go-fleettools", "--offline",
		"--floor", "1.27.3", "--baseline", path)
	if code != 0 {
		t.Fatalf("an unchanged exposure must exit 0, got %d\n%s", code, out)
	}
	if !strings.Contains(out, "1 still exposed, 0 WORSE") {
		t.Fatal(out)
	}

	// ⛔ A CLONE ARRIVING EXPOSED MUST NOT FAIL THE GATE — the mistake this
	// command made for real, eight times, 80 minutes after its own baseline.
	fakeRepo(t, filepath.Join(root, "arrived"), "go-fleettools/arrived")
	write(t, filepath.Join(root, "arrived", "go.mod"), "module github.com/go-fleettools/arrived\n\ngo 1.27.2\n")
	code, out, _ = runCmd(t, "--dir", root, "--owners", "go-fleettools", "--offline",
		"--floor", "1.27.3", "--baseline", path)
	if code != 0 {
		t.Fatalf("a module the baseline never measured is not a regression, got %d\n%s", code, out)
	}
	if !strings.Contains(out, "new to the population (not a regression) github.com/go-fleettools/arrived") ||
		!strings.Contains(out, "1 module(s) entered the population exposed") {
		t.Fatal(out)
	}

	// ⛔ THE PLANTED REGRESSION: a module the baseline measured and found CLEAN,
	// pushed below the floor. A guard nobody has seen fire is a guard that may
	// not fire.
	write(t, filepath.Join(root, "clean", "go.mod"), "module github.com/go-fleettools/clean\n\ngo 1.27.1\n")
	code, out, _ = runCmd(t, "--dir", root, "--owners", "go-fleettools", "--offline",
		"--floor", "1.27.3", "--baseline", path)
	if code != 1 {
		t.Fatalf("a module that was clean and is exposed must exit 1, got %d\n%s", code, out)
	}
	if !strings.Contains(out, "WORSE github.com/go-fleettools/clean") ||
		!strings.Contains(out, "3 still exposed, 1 WORSE, 1 new to the population") {
		t.Fatal(out)
	}

	// And a baseline from another floor is refused rather than believed, with
	// the status that says "could not measure" and not "found nothing". The
	// wording of that refusal is asserted in
	// TestABaselineFromAnotherFloorOrModeIsRefused; what matters here is that
	// the command carries status 2 and not 0.
	if code, _, _ = runCmd(t, "--dir", root, "--owners", "go-fleettools", "--offline",
		"--floor", "1.27.4", "--baseline", path); code != 2 {
		t.Fatalf("a baseline from another floor must exit 2, got %d", code)
	}
}
