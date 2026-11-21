package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"
	"time"
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
			if r.Unpublished {
				unpublished++
			} else {
				published++
			}
		}
		if r.Unreleased {
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
	// Resolved is the version a consumer's `go install <module>@latest`
	// chooses, which is the highest release in the proxy's version list and
	// NOT whatever the @latest endpoint answers. From names the endpoint that
	// decided it, because the two disagree for a while after a release (#16).
	Resolved string
	From     Endpoint
	Verdict  Verdict

	// ⛔ THE CLASSIFICATION IS A FIELD, NOT THE NOTE'S TEXT. Four separate
	// rules used to discriminate on `Note == "unpublished"` — the exposure
	// split, the floor that gets judged, `byDirective`, `reposOf` — and a
	// fifth on `strings.Contains(Note, "UNRELEASED")`. So adding a word to a
	// note silently changed five verdicts, which is how #17's fix would have
	// introduced its own defect: the moment the note became "unpublished
	// (404)" every one of those rules stopped matching and the row moved from
	// housekeeping into the campaign for a reason nobody wrote down. The note
	// is prose for a human; these booleans are what the code asks.

	// Unpublished: the proxy CONFIRMED, on a second serial read, that nothing
	// is published. One 404 under a sweep does not set it.
	Unpublished bool
	// Unconfirmed: a first read answered 404/410 and the re-read could neither
	// confirm nor contradict it. Such a row is UNREADABLE, never clean.
	Unconfirmed bool
	// ProxyRead: the proxy answered and its go.mod parsed, so Published is a
	// measurement and not a blank left by a failure.
	ProxyRead bool
	// Unreleased: the working tree carries the fix and the published module
	// does not.
	Unreleased bool

	Note string
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
	NoPackages int // modules with no Go package a consumer could build
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
			// ⛔ AND A MODULE WE PUBLISH IS STILL NOT ONE ANYBODY INSTALLS: a
			// go.mod is not a package. A landing site's
			// go.mod exists to pin a theme module, and its floor binds no
			// consumer because there is nothing to install. Judged by the
			// PACKAGES, never by the repository's name — see packages.go (#19).
			hasPkgs, err := HasGoPackages(filepath.Dir(f))
			if err != nil {
				c.Unreadable++
				if o.verbose {
					fmt.Fprintf(errOut, "⚠ %s: could not look for Go packages: %v\n", short(f), err)
				}
				continue
			}
			if !hasPkgs {
				c.NoPackages++
				if o.verbose {
					fmt.Fprintf(errOut, "⚠ no packages: %s declares %s and holds no Go code a consumer could build\n",
						short(f), fl.Module)
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
			"%d published by another project, %d with no buildable Go package). "+
			"Nothing was measured, which is not the same as nothing being wrong",
			o.owners, where, c.GoMods, c.OutOfScope, c.NotInRepo, c.Foreign, c.NoPackages)
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
		if o.offline || r.Unpublished {
			judged = r.Local
		}
		r.Verdict = Judge(judged, o.floor)
		if !o.offline && floorUnknown(*r) {
			// ⛔ A PUBLISHED FLOOR THAT COULD NOT BE READ IS NOT A FLOOR OF
			// NOTHING. Without this, Published stays blank, Judge reads the
			// blank as `no-go`, and `no-go` is Exposed — so a proxy that
			// answered nothing at all produced a full campaign.
			//
			// Measured 2026-11-15 and the reason this is here: a sweep of
			// 1229 modules in which EVERY read failed with "no route to host"
			// reported "1229 modules declare a floor below 1.27.4 … of those,
			// 1229 are PUBLISHED on the proxy (a consumer can install them) …
			// 1091 pull requests", and exited 1. Coherent, plausible, and
			// false in every figure. The proxy answered 200 for the first
			// module on that list two minutes later.
			r.Verdict = Unreadable
			continue
		}
		// A fix that exists locally but is not released yet is a different
		// state from a fix nobody has written, and it is the state a
		// remediation campaign needs to see.
		//
		// The guard is `ProxyRead`, not an empty note: it asks whether
		// Published is a measurement. A proxy failure leaves Published blank,
		// which Judge reads as `no-go` and would therefore label "fixed
		// locally" on the strength of a read that never happened — while a
		// module whose PUBLISHED go.mod genuinely carries no `go` directive is
		// a real instance of this state and must keep the label.
		if r.Verdict.Exposed() && !o.offline && r.ProxyRead &&
			r.Local.Go != "" && !Judge(r.Local, o.floor).Exposed() {
			r.Unreleased = true
			r.Note = "fixed locally, UNRELEASED"
		}
	}

	// ⛔ NOTHING ANOTHER TOOL CONSUMES IS DERIVED FROM A FLOOR NOBODY READ.
	// A `--repos` list feeds `gopin`, a baseline becomes the campaign's
	// statement of record, and both would carry the gap onward with no note
	// attached to it. The plain table is printed anyway — its NOTE column is
	// the diagnosis — and the refusal comes after it.
	if o.repos || o.writeBaseline != "" || o.baseline != "" {
		if err := unreadErr(rows, o); err != nil {
			return 2, c, err
		}
	}

	// The repositories, for `gopin --list`. Printed INSTEAD of the table, and
	// before it, because the answer is meant to be redirected into a file that
	// another tool reads: a table above it would be in that file too.
	if o.repos {
		n, err := reposOf(out, rows, o.declares, o.offline)
		if err != nil {
			return 2, c, err
		}
		if n == 0 {
			// ⛔ An empty list is not an empty campaign: it is a `--declares`
			// that matches nothing, and printing nothing would read as "done".
			// Name the floors that DO occur, the way a tool should when it
			// refuses an unknown value.
			keys, count := byDirective(rows, o.offline)
			var have []string
			for _, k := range keys {
				have = append(have, fmt.Sprintf("%s (%d)", k, count[k]))
			}
			if len(have) == 0 {
				return 2, c, fmt.Errorf("no module of this population is exposed at floor %s, so there is "+
					"no repository to act on", o.floor)
			}
			return 2, c, fmt.Errorf("no exposed module declares %q; the floors that occur are: %s",
				o.declares, strings.Join(have, ", "))
		}
		return 0, c, nil
	}

	printModules(out, rows, o, c)

	// The table has been printed, so the per-row notes are in front of the
	// reader; now the run says it did not measure them, and exits 2 rather
	// than 1. "Found a problem" and "could not look" must not be the same
	// status, and here they would have been the same SENTENCE.
	if err := unreadErr(rows, o); err != nil {
		return 2, c, err
	}

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

// floorUnknown reports whether this row's PUBLISHED floor was never read: the
// proxy failed, or denied the module once and then could not confirm it.
//
// It is one predicate on purpose. The two causes read differently in the note
// and identically in the verdict, and separating them is how one of them ends
// up judged: this tool judges the published floor, so no published floor is no
// verdict, whatever the reason.
func floorUnknown(r Row) bool {
	return r.Unconfirmed || (!r.Unpublished && !r.ProxyRead)
}

// unreadModules names the modules whose published floor was never read,
// capped: the message has to fit on a terminal, and the count is the finding.
func unreadModules(rows []Row) []string {
	var names []string
	extra := 0
	for _, r := range rows {
		if !floorUnknown(r) {
			continue
		}
		if len(names) < 10 {
			names = append(names, r.Module)
			continue
		}
		extra++
	}
	if extra > 0 {
		names = append(names, fmt.Sprintf("and %d more", extra))
	}
	return names
}

// unreadErr is the refusal both call sites share: the number of modules whose
// published floor was never read, and what that makes of the run.
func unreadErr(rows []Row, o options) error {
	if o.offline || o.floor == "" {
		// Offline judges the WORKING TREE and reads no proxy, so there is no
		// published floor to be missing. With no floor there is no verdict to
		// be wrong about.
		return nil
	}
	names := unreadModules(rows)
	if len(names) == 0 {
		return nil
	}
	return fmt.Errorf("the published floor of %d of %d module(s) was never read, so no verdict "+
		"was reached for them and the counts above are about the rest: %s. ⛔ Without this "+
		"refusal a blank published floor reads as `no-go`, `no-go` is an exposure, and a proxy "+
		"that answered NOTHING produces a complete campaign — measured on 2026-11-15, when every "+
		"read of a 1229-module sweep failed and the report named 1091 pull requests",
		len(unreadRows(rows)), len(rows), strings.Join(names, ", "))
}

func unreadRows(rows []Row) []Row {
	var out []Row
	for _, r := range rows {
		if floorUnknown(r) {
			out = append(out, r)
		}
	}
	return out
}

// confirmPause is how long the confirmation pass waits before its first
// re-read, so that a throttling window has a chance to close. The reads
// themselves are serial, which is the condition the first pass did not have.
var confirmPause = 2 * time.Second

// sweepProxy is the seam a test replaces to point a whole run at a double.
// It exists because the confirmation rule below is only observable END TO END
// — a unit test of askProxyWith cannot see whether the verdict loop, the
// exposure split and the exit status agree with it, and that gap is how a
// mechanism gets built and never wired up.
var sweepProxy = NewProxy

func askProxy(rows []Row, jobs int) {
	askProxyWith(sweepProxy(), rows, jobs, confirmPause)
}

// askProxyWith fills in what the proxy serves for every row — concurrently —
// and then CONFIRMS every unpublished answer, serially.
//
// ⛔ ONE 404 UNDER A SWEEP IS A RUMOUR, NOT AN ANSWER (#17). A fleet-scale run
// labelled published, exposed modules "unpublished", and `exposure()` counts
// that as housekeeping rather than as consumer exposure: the finding was not
// lost and the run did not fail, it was RECLASSIFIED DOWNWARDS — and the
// per-module control passed, so the only run that was wrong was the big one.
// That is the shape of `a-scan-that-could-not-read-reported-zero`, with the
// extra sting that the small, cheap check agrees with the truth.
//
// The absence is therefore re-read once with nothing else in flight, after a
// pause. Two 404s are an answer. A disagreement keeps the PUBLISHED answer and
// SAYS the first read was retried, because a sweep that silently self-corrects
// teaches nobody and the next occurrence would be invisible. A re-read that
// fails leaves the row unconfirmed, which is unreadable — never housekeeping.
func askProxyWith(p *Proxy, rows []Row, jobs int, pause time.Duration) {
	if jobs < 1 {
		jobs = 1
	}
	ctx := context.Background()
	// Written at its own index by its own goroutine, so there is no race and
	// no lock: the second pass needs the status the first read answered.
	firstStatus := make([]int, len(rows))
	var wg sync.WaitGroup
	sem := make(chan struct{}, jobs)
	for i := range rows {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			firstStatus[i] = readPublished(ctx, p, &rows[i]).absent
		}(i)
	}
	wg.Wait()

	first := true
	for i := range rows {
		r := &rows[i]
		if firstStatus[i] == 0 {
			continue
		}
		if first {
			time.Sleep(pause)
			first = false
		}
		was := firstStatus[i]
		// Everything the first read derived is cleared, and the row's identity
		// is carried over by name. A whole-struct reset is deliberate: if a
		// field is ever added to Row and forgotten here it empties a column,
		// which a test sees, instead of leaking a stale derived value.
		*r = Row{Module: r.Module, Repo: r.Repo, Local: r.Local, LocalFile: r.LocalFile}
		got := readPublished(ctx, p, r)
		retried := fmt.Sprintf("a first read under %d concurrent queries answered %d", jobs, was)
		switch {
		case got.absent != 0:
			// Two independent reads agree: this module really is not there.
			r.Note = fmt.Sprintf("unpublished (%d, re-read %d)", was, got.absent)
			r.Unpublished = true
		case got.exists && r.Note == "":
			r.Note = "published on re-read; " + retried
		case got.exists:
			// The module is there and a LATER step failed — an unparseable
			// published go.mod, a version that moved between the two calls.
			// The first read was still wrong about the module existing.
			r.Note = fmt.Sprintf("%s; published on re-read, %s", r.Note, retried)
		default:
			r.Note = fmt.Sprintf("unpublished (%d) UNCONFIRMED: the re-read neither "+
				"confirmed nor contradicted it (%s)", was, r.Note)
			r.Unconfirmed = true
		}
	}
}

// A proxyRead is what ONE read of the proxy established, which is three
// outcomes and not two.
//
// ⛔ THE THIRD ONE IS THE WHOLE POINT. A read that failed is not a read that
// found nothing: #17 is exactly the second being reported as the first. So
// "the proxy says it is not there", "the module is there" and "nothing was
// established" are separate answers here, and the confirmation pass needs all
// three — a network error on a re-read proves nothing about the module, and
// must not be allowed to confirm, or to contradict, the first read.
type proxyRead struct {
	absent int  // nonzero: the status with which the proxy denied the module
	exists bool // the proxy resolved a version, so the module IS on the proxy
}

// readPublished fills one row from the proxy and says what it established.
// Notes for the steps after @latest are written onto the row.
func readPublished(ctx context.Context, p *Proxy, r *Row) proxyRead {
	ver, from, err := p.Resolve(ctx, r.Module)
	if st := unpublishedStatus(err); st != 0 {
		return proxyRead{absent: st}
	}
	if errors.Is(err, ErrUnpublished) {
		// A sentinel carrying no status: this proxy never produces one, but a
		// test double or a later caller can, and reading it as anything other
		// than an absence would be the original defect in a new place.
		return proxyRead{absent: http.StatusNotFound}
	}
	if err != nil {
		r.Note = "proxy: " + err.Error()
		return proxyRead{}
	}
	// From here the module exists whatever else fails: the proxy answered for it.
	r.Resolved = ver
	r.From = from
	src, err := p.Mod(ctx, r.Module, ver)
	if err != nil {
		r.Note = "proxy: " + err.Error()
		return proxyRead{exists: true}
	}
	fl, err := ParseFloors(src)
	if err != nil {
		r.Note = "published go.mod unparseable: " + err.Error()
		return proxyRead{exists: true}
	}
	r.Published = fl
	r.ProxyRead = true
	return proxyRead{exists: true}
}

func printModules(out io.Writer, rows []Row, o options, c Census) {
	w := tabwriter.NewWriter(out, 0, 8, 2, ' ', 0)
	// RESOLVED, not @LATEST: the column holds the version a consumer installs,
	// which is resolved from the version list. Naming it after the endpoint it
	// no longer reads would be a header that misreports its own column.
	head := "REPOSITORY\tMODULE\tLOCAL\tPUBLISHED\tRESOLVED"
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
		res := dash(r.Resolved)
		// An untagged module was resolved through the fallback endpoint, and
		// saying so costs one word and saves the reader a guess.
		if r.From == FromLatest && r.Resolved != "" {
			res += " (@latest)"
		}
		line := fmt.Sprintf("%s\t%s\t%s\t%s\t%s",
			dash(r.Repo.Slug()), r.Module, dash(r.Local.Go), pub, res)
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
		"%d with no buildable Go package, %d without a module directive, %d unreadable\n",
		o.owners, c.GoMods, c.Modules, c.OutOfScope, c.NotInRepo, c.Foreign,
		c.NoPackages, c.NoModule, c.Unreadable)

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
		// ⛔ AND THE SHAPE OF THE WORK, not only its size: `gopin --gomod
		// --from X` names the version to move EXACTLY, so a campaign over
		// modules declaring several floors is one run per floor.
		keys, count := byDirective(rows, o.offline)
		fmt.Fprintf(out, "   they declare %d different floors, so `gopin --gomod --from X "+
			"--version %s` is %d runs and not one:\n", len(keys), o.floor, len(keys))
		for _, k := range keys {
			fmt.Fprintf(out, "      --from %-10s %d module(s)\n", k, count[k])
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
