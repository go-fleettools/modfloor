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
// building from a clone. Measured on this fleet on 2026-11-01: 262 of 348
// modules were below the floor, every one of them published.
//
// ⛔ The figure first recorded here was "935 of 1025", and it was wrong: the
// population included 540 modules other projects publish, found inside our own
// work trees. Repo.Declares now sets those aside. A true count over the wrong
// population is worse than no count, and it was published on two pages before
// the tool's own first baseline showed sigs.k8s.io/yaml in it.
//
// "fixed locally, UNRELEASED" is counted apart again, because it is the state
// where the pull request is merged and the work is NOT finished: the consumer
// reads the published go.mod, not the branch.
// ⛔ A CAMPAIGN IS COSTED IN PULL REQUESTS, NOT IN FINDINGS. 262 exposed modules
// reads as 262 pieces of work, and it is not: several modules of one repository
// are raised by ONE pull request, because the `go` directive they share lives in
// go.mod files that land together. Measured on a sibling campaign, 896 stale
// pins were 123 pull requests — a factor of seven. A count nobody can turn into
// an amount of work cannot be scheduled, and an estimate seven times too large
// is an estimate that gets the campaign refused.
//
// A module named as a positional argument has no work tree, so there is no
// repository to credit it to: those are counted apart rather than folded into
// the estimate, since a module we did not find on disk is one we may not be
// able to push to at all.
func campaign(rows []Row) (repos, unplaced int) {
	seen := map[string]bool{}
	for _, r := range rows {
		if !r.Verdict.Exposed() {
			continue
		}
		if slug := r.Repo.Slug(); slug != "" {
			seen[slug] = true
		} else {
			unplaced++
		}
	}
	return len(seen), unplaced
}

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
	Foreign    int // modules another project publishes, found inside one of ours
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
			// ⛔ AND THE SECOND HALF OF "CAN WE PUSH A FIX": the work tree
			// being ours does not make the MODULE ours. tannevaled/pantry
			// carries other projects' sources, so its go.mod files answered
			// the owner test and put sigs.k8s.io/yaml and cel.dev/expr into a
			// baseline of this fleet's exposures.
			if !repo.Declares(fl.Module) {
				c.Foreign++
				if o.verbose {
					fmt.Fprintf(errOut, "⚠ foreign: %s declares %s, which %s does not publish\n",
						short(f), fl.Module, repo.Slug())
				}
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
			"seen and all were set aside (%d out of scope, %d in no work tree, "+
			"%d published by another project). "+
			"Nothing was measured, which is not the same as nothing being wrong",
			o.owners, where, c.GoMods, c.OutOfScope, c.NotInRepo, c.Foreign)
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
	// though it just found hundreds of exposures, because the file it wrote is the
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
	// Measured 2026-11-01: 262 of 348 of this fleet's modules were below the
	// floor, and all 262 published — so here the split says the exposure is as
	// large as the count, which is an answer and not a reassurance.
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
		"%d out of scope, %d in no work tree, %d published by another project, "+
		"%d without a module directive, %d unreadable\n",
		o.owners, c.GoMods, c.Modules, c.OutOfScope, c.NotInRepo, c.Foreign, c.NoModule, c.Unreadable)

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
		// The size of the WORK, not of the finding: several modules of one
		// repository are raised by one pull request.
		repos, unplaced := campaign(rows)
		fmt.Fprintf(out, "   they live in %d repositories, so the campaign is about %d pull "+
			"requests and not %d", repos, repos, n)
		if unplaced > 0 {
			fmt.Fprintf(out, ", plus %d module(s) found in no work tree", unplaced)
		}
		fmt.Fprintln(out, ".")
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
