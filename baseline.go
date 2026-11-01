package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"
)

// ⛔ A REPORT THAT IS RED FOR EVER IS NOT A GATE. On 2026-11-01 this fleet had
// 262 published modules declaring a floor below 1.27.3, so
// `modfloor --floor 1.27.3` exits 1 every time it runs and will keep doing so
// for as long as the campaign to raise them takes. A lane in that state is read
// as broken, then ignored, and the day a NEW module lands below the floor
// nothing changes colour.
//
// A baseline separates the two questions a campaign actually has: "how many are
// exposed" (a number that drains slowly) and "did anything get WORSE since the
// last look" (a number that must stay zero). The second one is a gate.
//
// The file is deliberately a plain text one, so that the diff of a tightened
// baseline is readable in a pull request:
//
//	# modfloor baseline
//	floor 1.27.3
//	mode published
//	taken 2026-11-01
//	BELOW   github.com/go-widgets/toolkit
//	no-go   example.com/ancient
type Baseline struct {
	Floor   string            // the floor this baseline was taken against
	Mode    string            // "published" or "offline": which go.mod was judged
	Taken   string            // the date, informational
	Exposed map[string]string // module path -> the verdict recorded for it
}

// baselineMode names the go.mod a run judges, which is half of what makes a
// baseline comparable: the published floor and the working tree's floor are two
// different populations of answers about the same modules.
func baselineMode(offline bool) string {
	if offline {
		return "offline"
	}
	return "published"
}

// WriteBaseline records the exposed set of one run.
func WriteBaseline(w io.Writer, rows []Row, floor string, offline bool) error {
	exposed := make([]Row, 0, len(rows))
	for _, r := range rows {
		if r.Verdict.Exposed() {
			exposed = append(exposed, r)
		}
	}
	sort.Slice(exposed, func(i, j int) bool { return exposed[i].Module < exposed[j].Module })

	// ⛔ A MODULE PATH THAT CARRIES THE FILE'S OWN SEPARATORS COULD WIDEN THE
	// BASELINE. The paths come from go.mod files on disk, and this file is read
	// back as a GATE: a path holding a newline would land as an extra line, so
	// a module nobody recorded would read as already known and its exposure
	// would pass unseen. The same shape as the traversal a module path must not
	// carry into a proxy URL, which this tool already refuses. Fail closed.
	for _, r := range exposed {
		if strings.ContainsAny(r.Module, "\t\n\r") {
			return fmt.Errorf("module path %q carries a tab or a newline, which are this file's "+
				"separators: refusing to write a baseline a later run would misread", r.Module)
		}
	}

	b := &strings.Builder{}
	fmt.Fprintln(b, "# modfloor baseline — the exposed set at a KNOWN floor, so that a later")
	fmt.Fprintln(b, "# run can answer \"did anything get worse\" instead of \"how many are there\".")
	fmt.Fprintln(b, "# Tighten it when modules are fixed; never widen it to make a lane green.")
	fmt.Fprintf(b, "floor %s\n", floor)
	fmt.Fprintf(b, "mode %s\n", baselineMode(offline))
	fmt.Fprintf(b, "taken %s\n", time.Now().UTC().Format("2006-01-02"))
	for _, r := range exposed {
		fmt.Fprintf(b, "%s\t%s\n", r.Verdict, r.Module)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// ReadBaseline parses a baseline file.
//
// ⛔ It REFUSES a file with no floor line. A baseline is a set of module names,
// and a set of module names is meaningless without the floor it was judged
// against: every verdict in this tool changes the day the floor moves, and it
// moved three times in four days (1.27.1 → 1.27.2 → 1.27.3). A nameless
// baseline would silently keep passing a gate whose question had changed.
func ReadBaseline(r io.Reader) (*Baseline, error) {
	b := &Baseline{Exposed: map[string]string{}}
	sc := bufio.NewScanner(r)
	for n := 1; sc.Scan(); n++ {
		// ⛔ Spaces and a stray carriage return are trimmed, NOT the tab: the
		// tab is the field separator, and trimming it turned "BELOW\t" — a
		// verdict whose module path is missing — into something that parsed as
		// a header line and complained about the wrong thing.
		line := strings.TrimRight(sc.Text(), " \r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if verdict, module, ok := strings.Cut(line, "\t"); ok {
			module = strings.TrimSpace(module)
			if module == "" {
				return nil, fmt.Errorf("line %d: a verdict with no module path", n)
			}
			b.Exposed[module] = strings.TrimSpace(verdict)
			continue
		}
		key, value, ok := strings.Cut(line, " ")
		if !ok {
			return nil, fmt.Errorf("line %d: %q is neither a header nor a verdict and a module path separated by a tab", n, line)
		}
		switch key {
		case "floor":
			b.Floor = value
		case "mode":
			b.Mode = value
		case "taken":
			b.Taken = value
		default:
			return nil, fmt.Errorf("line %d: unknown header %q", n, key)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if b.Floor == "" {
		return nil, fmt.Errorf("no `floor` line: a baseline that does not say which floor it was " +
			"taken against cannot judge any floor, because every verdict changes when the floor moves")
	}
	if b.Mode == "" {
		return nil, fmt.Errorf("no `mode` line: a baseline must say whether it recorded the PUBLISHED " +
			"floors or the working trees' (--offline), because those are different answers about the same modules")
	}
	if b.Mode != "published" && b.Mode != "offline" {
		return nil, fmt.Errorf("mode %q: expected `published` or `offline`", b.Mode)
	}
	return b, nil
}

// A Ratchet is the comparison of one run against a baseline.
type Ratchet struct {
	// Regressed are exposed modules the baseline did not have: the gate.
	Regressed []string
	// Fixed were in the baseline, were measured again, and are no longer
	// exposed. Real progress, and the reason to tighten the file.
	Fixed []string
	// Gone were in the baseline and were NOT MEASURED AT ALL this time.
	//
	// ⛔ These are kept apart from Fixed and never counted as progress. A
	// module drops out of the population when its clone is deleted, renamed,
	// moved out of the owner globs, or simply not walked — and a vanished
	// module looks exactly like a repaired one to anything that only subtracts
	// two sets. Reporting them together is how a campaign declares victory by
	// losing track of its subjects.
	Gone []string
}

// Compare judges one run's rows against a baseline. measured names every module
// the run actually looked at, which is what separates a fix from a disappearance.
func Compare(b *Baseline, rows []Row) Ratchet {
	var rt Ratchet
	measured := make(map[string]bool, len(rows))
	exposed := make(map[string]bool, len(rows))
	for _, r := range rows {
		measured[r.Module] = true
		if r.Verdict.Exposed() {
			exposed[r.Module] = true
			if _, known := b.Exposed[r.Module]; !known {
				rt.Regressed = append(rt.Regressed, r.Module)
			}
		}
	}
	for m := range b.Exposed {
		switch {
		case !measured[m]:
			rt.Gone = append(rt.Gone, m)
		case !exposed[m]:
			rt.Fixed = append(rt.Fixed, m)
		}
	}
	sort.Strings(rt.Regressed)
	sort.Strings(rt.Fixed)
	sort.Strings(rt.Gone)
	return rt
}

// printRatchet reports the comparison and returns the status it carries: 1 when
// something regressed, 0 otherwise. An unchanged exposure is NOT reported as
// clean — the line says how many are still exposed, so that a green lane cannot
// be mistaken for an empty campaign.
func printRatchet(out io.Writer, b *Baseline, rt Ratchet, stillExposed int) int {
	fmt.Fprintf(out, "\nratchet against a baseline of %d exposed module(s), floor %s, mode %s, taken %s:\n",
		len(b.Exposed), b.Floor, b.Mode, b.Taken)
	if len(rt.Regressed) > 0 {
		fmt.Fprintf(out, "⛔ %d module(s) are exposed that the baseline did not have:\n", len(rt.Regressed))
		for _, m := range rt.Regressed {
			fmt.Fprintln(out, "   NEW ", m)
		}
	}
	for _, m := range rt.Fixed {
		fmt.Fprintln(out, "   fixed", m)
	}
	// Said separately, and never added to the fixed ones: a module that left the
	// population was not repaired, it stopped being looked at.
	for _, m := range rt.Gone {
		fmt.Fprintln(out, "   NOT MEASURED THIS TIME (not a fix)", m)
	}
	fmt.Fprintf(out, "%d still exposed, %d new, %d fixed, %d no longer measured\n",
		stillExposed, len(rt.Regressed), len(rt.Fixed), len(rt.Gone))
	if len(rt.Regressed) > 0 {
		return 1
	}
	if len(rt.Fixed) > 0 {
		fmt.Fprintf(out, "   %d fixed: rerun with --write-baseline to tighten the file, or the next "+
			"regression into one of them passes unseen.\n", len(rt.Fixed))
	}
	return 0
}

// checkBaseline is the flag combinations the ratchet cannot answer, refused
// before anything is measured.
func checkBaseline(o options) error {
	if o.baseline != "" && o.writeBaseline != "" {
		return fmt.Errorf("--baseline and --write-baseline in one run: recording the answer and then " +
			"judging it against itself always passes, which is a gate that cannot fail")
	}
	if (o.baseline != "" || o.writeBaseline != "") && o.floor == "" {
		return fmt.Errorf("a baseline is a set of modules exposed AT A FLOOR: pass --floor")
	}
	if (o.baseline != "" || o.writeBaseline != "") && o.dir == "" && len(o.modules) == 0 {
		return fmt.Errorf("a baseline is about modules: pass --dir or module paths")
	}
	return nil
}

// loadBaseline reads the file and refuses one that answers a different question
// from the run it is about to judge.
func loadBaseline(path, floor string, offline bool) (*Baseline, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := ReadBaseline(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if b.Floor != floor {
		return nil, fmt.Errorf("%s was taken against floor %s and this run judges floor %s: "+
			"every verdict changes when the floor moves, so the two sets are not comparable. "+
			"Re-record it with --write-baseline --floor %s", path, b.Floor, floor, floor)
	}
	if want := baselineMode(offline); b.Mode != want {
		return nil, fmt.Errorf("%s recorded the %s floors and this run judges the %s ones: "+
			"those are different answers about the same modules", path, b.Mode, want)
	}
	return b, nil
}
