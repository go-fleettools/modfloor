package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"
)

// exposure splits the modules a floor condemned by whether anybody can reach
// them, and counts the ones already fixed but not released.
//
// ⛔ ONE NUMBER OVER BOTH POPULATIONS CANNOT BE PRIORITISED, which is why this
// exists. A module on the proxy imposes its floor on every consumer that runs
// `go install`; one that was never published imposes it only on somebody
// building from a clone. Measured on this fleet on 2026-10-31: 935 of 1025
// modules were below the floor, and that reads as a thousand-repository
// emergency until the split says how many a consumer can actually install.
//
// "fixed locally, UNRELEASED" is counted apart again, because it is the state
// where the pull request is merged and the work is NOT finished: the consumer
// reads the published go.mod, not the branch.
func exposure(rows []Row) (published, unpublished, fixedUnreleased int) {
	for _, r := range rows {
		if r.Verdict.Exposed() {
			if r.Note == "unpublished" {
				unpublished++
			} else {
				published++
			}
		}
		if strings.Contains(r.Note, "UNRELEASED") {
			fixedUnreleased++
		}
	}
	return published, unpublished, fixedUnreleased
}

// A Row is one module's answer: what the working tree declares, what the proxy
// serves, and the verdict — which is judged on the PUBLISHED floor, because
// that is the one a consumer honours.
type Row struct {
	Module    string
	Repo      Repo   // the work tree it was found in: who can push a fix
	Local     Floors // from the working tree, if a clone was found
	LocalFile string
	Published Floors // from the proxy's copy of @latest
	Latest    string // the version @latest resolved to
	Verdict   Verdict
	Note      string
}

// A Census is what the walk found and what it set aside, kept apart from the
// answer: a scan that could not read, or deliberately skipped, most of a tree
// must not be reported as a measurement of that tree.
type Census struct {
	GoMods     int // go.mod files seen
	NoModule   int // files with no module directive
	Unreadable int // files and directories that could not be read
	OutOfScope int // modules whose owner is not in the declared population
	NotInRepo  int // modules in no git work tree at all: caches, archives
	Modules    int // distinct modules kept
}

// reportModules measures the go directive of every module in the declared
// population and returns the exit code the caller should carry: 0 clean,
// 1 at least one module exposed, 2 the measurement could not be made.
func reportModules(out, errOut io.Writer, o options) (int, Census, error) {
	// Fold by module path: the same module appears once per worktree, and
	// counting worktrees would inflate every number in the report.
	local := map[string]Row{}
	var c Census

	if o.dir != "" {
		files, problems, err := FindGoMods(o.dir)
		if err != nil {
			return 2, c, fmt.Errorf("walking %s: %w", o.dir, err)
		}
		c.Unreadable += len(problems)
		for _, f := range problems {
			if o.verbose {
				fmt.Fprintln(errOut, "⚠ unreadable:", f)
			}
		}
		c.GoMods = len(files)
		finder := newRepoFinder()
		for _, f := range files {
			src, err := readCapped(f)
			if err != nil {
				c.Unreadable++
				fmt.Fprintln(errOut, "⚠ unreadable:", f, err)
				continue
			}
			fl, err := ParseFloors(src)
			if err != nil {
				// A go.mod the toolchain itself cannot parse is a finding, not
				// a module with no floor: the two call for different fixes, so
				// it is counted as unreadable and named.
				c.Unreadable++
				fmt.Fprintln(errOut, "⚠ unparseable:", f, err)
				continue
			}
			if fl.Module == "" {
				c.NoModule++
				continue
			}
			// Scope FIRST, so that nothing downstream — not a warning, not a
			// count, not a verdict — ever speaks about a module we cannot fix.
			repo, err := finder.Of(filepath.Dir(f))
			if errors.Is(err, ErrNoRepo) {
				c.NotInRepo++
				continue
			}
			if err != nil {
				c.Unreadable++
				if o.verbose {
					fmt.Fprintln(errOut, "⚠", f, err)
				}
				continue
			}
			if !o.owners.Match(repo.Owner) {
				c.OutOfScope++
				continue
			}

			// First clone wins; a second is noted only if it disagrees, since
			// two worktrees of one repository legitimately differ mid-branch.
			if prev, ok := local[fl.Module]; ok {
				if prev.Local.Go != fl.Go && o.verbose {
					fmt.Fprintf(errOut, "⚠ %s declares %s in %s and %s in %s\n",
						fl.Module, prev.Local.Go, short(prev.LocalFile), fl.Go, short(f))
				}
				continue
			}
			local[fl.Module] = Row{Module: fl.Module, Repo: repo, Local: fl, LocalFile: f}
		}
	}
	for _, m := range o.modules {
		if _, ok := local[m]; !ok {
			local[m] = Row{Module: m}
		}
	}

	c.Modules = len(local)

	// A measurement that found nothing must not be reported as a clean fleet:
	// zero exposed modules out of zero modules is the best possible value in
	// its column, which is exactly how an unopened channel reads as perfection.
	if len(local) == 0 {
		where := o.dir
		if where == "" {
			where = "the arguments"
		}
		return 2, c, fmt.Errorf("no module of %s found in %s — %d go.mod files were "+
			"seen and all were set aside (%d out of scope, %d in no work tree). "+
			"Nothing was measured, which is not the same as nothing being wrong",
			o.owners, where, c.GoMods, c.OutOfScope, c.NotInRepo)
	}

	rows := make([]Row, 0, len(local))
	for _, r := range local {
		rows = append(rows, r)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Module < rows[j].Module })

	if o.listOnly {
		for _, r := range rows {
			fmt.Fprintln(out, r.Module)
		}
		return 0, c, nil
	}

	if !o.offline {
		askProxy(rows, o.jobs)
	}

	for i := range rows {
		r := &rows[i]
		if o.floor == "" {
			continue
		}
		judged := r.Published
		if o.offline || r.Note == "unpublished" {
			judged = r.Local
		}
		r.Verdict = Judge(judged, o.floor)
		// A fix that exists locally but is not released yet is a different
		// state from a fix nobody has written, and it is the state a
		// remediation campaign needs to see.
		if r.Verdict.Exposed() && !o.offline && r.Note == "" &&
			r.Local.Go != "" && !Judge(r.Local, o.floor).Exposed() {
			r.Note = "fixed locally, UNRELEASED"
		}
	}

	printModules(out, rows, o, c)

	exposed := 0
	for _, r := range rows {
		if r.Verdict.Exposed() {
			exposed++
		}
	}

	// Recording is not judging: a run that writes the baseline exits 0 even
	// though it just found 904 exposures, because the file it wrote is the
	// statement of where the campaign starts, not a verdict on it.
	if o.writeBaseline != "" {
		f, err := os.Create(o.writeBaseline)
		if err != nil {
			return 2, c, err
		}
		if err := WriteBaseline(f, rows, o.floor, o.offline); err != nil {
			f.Close()
			return 2, c, err
		}
		if err := f.Close(); err != nil {
			return 2, c, err
		}
		fmt.Fprintf(out, "wrote %d exposed module(s) to %s at floor %s, mode %s\n",
			exposed, o.writeBaseline, o.floor, baselineMode(o.offline))
		return 0, c, nil
	}
	if o.baseline != "" {
		b, err := loadBaseline(o.baseline, o.floor, o.offline)
		if err != nil {
			return 2, c, err
		}
		return printRatchet(out, b, Compare(b, rows), exposed), c, nil
	}

	if o.floor != "" && exposed > 0 {
		return 1, c, nil
	}
	return 0, c, nil
}

func askProxy(rows []Row, jobs int) {
	if jobs < 1 {
		jobs = 1
	}
	p := NewProxy()
	ctx := context.Background()
	var wg sync.WaitGroup
	sem := make(chan struct{}, jobs)
	for i := range rows {
		wg.Add(1)
		go func(r *Row) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			ver, err := p.Latest(ctx, r.Module)
			if errors.Is(err, ErrUnpublished) {
				r.Note = "unpublished"
				return
			}
			if err != nil {
				r.Note = "proxy: " + err.Error()
				return
			}
			r.Latest = ver
			src, err := p.Mod(ctx, r.Module, ver)
			if err != nil {
				r.Note = "proxy: " + err.Error()
				return
			}
			fl, err := ParseFloors(src)
			if err != nil {
				r.Note = "published go.mod unparseable: " + err.Error()
				return
			}
			r.Published = fl
		}(&rows[i])
	}
	wg.Wait()
}

func printModules(out io.Writer, rows []Row, o options, c Census) {
	w := tabwriter.NewWriter(out, 0, 8, 2, ' ', 0)
	head := "REPOSITORY\tMODULE\tLOCAL\tPUBLISHED\t@LATEST"
	if o.floor != "" {
		head += "\tVERDICT"
	}
	fmt.Fprintln(w, head+"\tNOTE")

	tally := map[Verdict]int{}
	// ⛔ AND THE EXPOSED ONES SPLIT BY WHETHER ANYBODY CAN INSTALL THEM, because
	// one number over both populations cannot be prioritised. A module on the
	// proxy imposes its floor on every consumer that runs `go install`; one that
	// was never published imposes it only on somebody building from a clone.
	// Measured 2026-10-31: 935 of 1025 of this fleet's modules were below the
	// floor, which reads as a thousand-repository emergency until the split says
	// how many a consumer can actually reach.
	exposedPublished, exposedUnpublished, fixedUnreleased := exposure(rows)
	for _, r := range rows {
		pub := dash(r.Published.Go)
		if o.offline {
			pub = "—"
		}
		line := fmt.Sprintf("%s\t%s\t%s\t%s\t%s",
			dash(r.Repo.Slug()), r.Module, dash(r.Local.Go), pub, dash(r.Latest))
		if o.floor != "" {
			line += "\t" + string(r.Verdict)
			tally[r.Verdict]++
		}
		fmt.Fprintln(w, line+"\t"+r.Note)
	}
	w.Flush()

	// The population this report is about, printed next to its numbers, because
	// a count is only as meaningful as the set it counts over — and the set was
	// declared on the command line, not guessed.
	fmt.Fprintf(out, "\npopulation: owners %s — %d go.mod seen, %d kept; set aside: "+
		"%d out of scope, %d in no work tree, %d without a module directive, %d unreadable\n",
		o.owners, c.GoMods, c.Modules, c.OutOfScope, c.NotInRepo, c.NoModule, c.Unreadable)

	if o.floor == "" {
		fmt.Fprintf(out, "no floor given, so no verdict. Pass --floor 1.27.2 for one.\n")
		return
	}
	fmt.Fprintf(out, "%d modules against floor %s:", len(rows), o.floor)
	for _, v := range []Verdict{OK, Pinned, Below, NoFloor, Unreadable} {
		if tally[v] > 0 {
			fmt.Fprintf(out, "  %s=%d", v, tally[v])
		}
	}
	fmt.Fprintln(out)
	if n := tally[Below] + tally[NoFloor]; n > 0 {
		fmt.Fprintf(out, "⛔ %d modules declare a floor below %s — a consumer running "+
			"`go install` with GOTOOLCHAIN=local builds unpatched code.\n", n, o.floor)
		// Which of them a consumer can actually reach, said separately: the
		// published ones are the campaign, the rest are housekeeping.
		if !o.offline {
			fmt.Fprintf(out, "   of those, %d are PUBLISHED on the proxy (a consumer can install them) "+
				"and %d were never published (only a local build honours their floor).\n",
				exposedPublished, exposedUnpublished)
			if fixedUnreleased > 0 {
				fmt.Fprintf(out, "   and %d already carry the fix in their working tree but have not been "+
					"RELEASED, so the pull request is merged and the work is not finished.\n", fixedUnreleased)
			}
		}
	}
}

// reportBinaries measures the toolchain that BUILT each installed executable,
// which is a different question from the floor a module declares and fails
// independently of it.
func reportBinaries(out, errOut io.Writer, dir, floor string) error {
	walk, err := ReadBinaries(dir)
	if err != nil {
		return err
	}
	if len(walk.Binaries) == 0 {
		return fmt.Errorf("no Go executable in %s (%d files carried no build info) — nothing was measured", dir, walk.NotGo)
	}
	w := tabwriter.NewWriter(out, 0, 8, 2, ' ', 0)
	fmt.Fprintln(w, "BINARY\tMODULE\tVERSION\tBUILT WITH")
	for _, b := range walk.Binaries {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
			filepath.Base(b.File), b.Module, dash(b.Version), dash(b.GoVersion))
	}
	w.Flush()

	stale := 0
	if floor != "" {
		for _, b := range walk.Binaries {
			if Judge(Floors{Go: b.GoVersion}, floor).Exposed() {
				stale++
			}
		}
	}
	// Completeness is reported next to the answer, never folded into it.
	fmt.Fprintf(out, "\n%d Go executables in %s; %d files carried no build info",
		len(walk.Binaries), dir, walk.NotGo)
	if floor != "" {
		fmt.Fprintf(out, "; %d built below %s", stale, floor)
	}
	fmt.Fprintln(out)
	for _, e := range walk.Errors {
		fmt.Fprintln(errOut, "⚠", e)
	}
	fmt.Fprintln(out)
	return nil
}

func dash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func short(p string) string {
	if d := filepath.Dir(p); d != "" {
		return filepath.Base(d) + "/go.mod"
	}
	return p
}
