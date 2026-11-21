package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// A proxyDouble answers like the module proxy, and can be told to deny a
// module for its first N reads of @latest.
//
// ⛔ IT COUNTS THE READS BECAUSE THE DEFECT IS ABOUT THE SECOND ONE. #17 is a
// first read that answered 404 for a module that was there; a double that
// always answers the same thing cannot witness either the defect or its fix.
type proxyDouble struct {
	srv *httptest.Server
	// ⛔ THE DOUBLE IS SERVED FROM ONE GOROUTINE PER CONNECTION, so every map
	// below is shared mutable state. Without this lock `go test -race` reports
	// a data race in the INSTRUMENT — which CI did, at 76d75f5, while the
	// local suite ran without -race and passed. The thing under test here is
	// concurrency; a double that is not safe under it cannot measure it.
	mu sync.Mutex
	// denyFirst[module] reads left to deny before answering truthfully.
	denyFirst map[string]int
	// status is what a denial answers with: 404 by default, 410 for a module
	// the proxy says is gone.
	status map[string]int
	// published[module] is the `go` directive the published go.mod carries.
	published map[string]string
	// failWith[module], if set, is a status answered INSTEAD of the truth once
	// the denials run out: a read that establishes nothing.
	failWith map[string]int
	// reads[module] counts the reads of the endpoint a resolution STARTS from.
	// That endpoint moved from @latest to @v/list when #16 was fixed, and this
	// counter followed it: it exists to witness the confirmation re-read, which
	// is the first read repeated, whichever endpoint that is.
	reads map[string]int
	// versions[module] is the body @v/list answers. Absent means the default
	// one-release list; an entry present and EMPTY means a published module
	// with no tag, which is what drives the @latest fallback.
	versions map[string][]string
	// latestReads[module] counts reads of the @latest endpoint alone, so a test
	// can assert the fallback was NOT consulted — one extra request per module
	// over a fleet sweep is a cost, not a detail.
	latestReads map[string]int
	// latestFailWith[module], if set, is a status the @latest endpoint answers
	// while the LIST still answers. It exists because the fallback has its own
	// failure path: the list can be legitimately empty and the fallback read
	// then fail, and a double that can only fail both at once cannot reach it.
	latestFailWith map[string]int
}

func newProxyDouble(t *testing.T) *proxyDouble {
	t.Helper()
	d := &proxyDouble{
		denyFirst:      map[string]int{},
		status:         map[string]int{},
		published:      map[string]string{},
		failWith:       map[string]int{},
		reads:          map[string]int{},
		versions:       map[string][]string{},
		latestReads:    map[string]int{},
		latestFailWith: map[string]int{},
	}
	d.srv = httptest.NewServer(http.HandlerFunc(d.serve))
	t.Cleanup(d.srv.Close)
	return d
}

func (d *proxyDouble) proxy() *Proxy {
	return &Proxy{Base: d.srv.URL, Client: d.srv.Client()}
}

func (d *proxyDouble) serve(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	defer d.mu.Unlock()
	path := strings.TrimPrefix(r.URL.Path, "/")
	switch {
	case strings.HasSuffix(path, "/@v/list"):
		mod := strings.TrimSuffix(path, "/@v/list")
		d.reads[mod]++
		if d.denyFirst[mod] > 0 {
			d.denyFirst[mod]--
			code := d.status[mod]
			if code == 0 {
				code = http.StatusNotFound
			}
			w.WriteHeader(code)
			return
		}
		if code := d.failWith[mod]; code != 0 {
			w.WriteHeader(code)
			return
		}
		vers, ok := d.versions[mod]
		if !ok {
			vers = []string{"v1.2.3"}
		}
		for _, v := range vers {
			fmt.Fprintln(w, v)
		}
	case strings.HasSuffix(path, "/@latest"):
		mod := strings.TrimSuffix(path, "/@latest")
		d.latestReads[mod]++
		if code := d.latestFailWith[mod]; code != 0 {
			w.WriteHeader(code)
			return
		}
		if code := d.failWith[mod]; code != 0 {
			w.WriteHeader(code)
			return
		}
		fmt.Fprint(w, `{"Version":"v1.2.3"}`)
	case strings.HasSuffix(path, "/@v/v1.2.3.mod"):
		mod := strings.TrimSuffix(path, "/@v/v1.2.3.mod")
		got := d.published[mod]
		if got == "" {
			got = "1.27.1"
		}
		fmt.Fprintf(w, "module %s\n\ngo %s\n", mod, got)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// ⛔ THE DEFECT ITSELF. A fleet-scale sweep labelled published, exposed modules
// "unpublished", which the exposure split counts as housekeeping rather than as
// consumer exposure — a finding RECLASSIFIED DOWNWARDS, never failing, while
// the per-module control passed. One 404 under concurrency is a rumour.
func TestAFourOhFourUnderASweepIsRetriedRatherThanBelieved(t *testing.T) {
	d := newProxyDouble(t)
	d.denyFirst["example.com/a"] = 1
	d.published["example.com/a"] = "1.27.1"

	rows := []Row{{Module: "example.com/a"}}
	askProxyWith(d.proxy(), rows, 8, 0)

	r := rows[0]
	if r.Unpublished {
		t.Error("one 404 was believed: the row is marked unpublished, which the " +
			"exposure split counts as housekeeping")
	}
	if r.Unconfirmed {
		t.Error("the re-read succeeded, so the row is not unconfirmed")
	}
	if r.Published.Go != "1.27.1" || r.Resolved != "v1.2.3" || !r.ProxyRead {
		t.Errorf("the published floor was not read: %+v", r)
	}
	if d.reads["example.com/a"] != 2 {
		t.Errorf("the version list was read %d times, want 2 (the first and its confirmation)",
			d.reads["example.com/a"])
	}
	// ⛔ AND IT SAYS SO. A sweep that silently self-corrects teaches nobody,
	// and the next occurrence would be invisible.
	for _, want := range []string{"re-read", "404", "concurrent"} {
		if !strings.Contains(r.Note, want) {
			t.Errorf("note %q does not say it retried (%q missing)", r.Note, want)
		}
	}
}

// Two independent reads that agree are an answer, and that answer must reach
// the exposure split as housekeeping.
func TestTwoDenialsAreAnAnswerAndTheyNameTheirStatus(t *testing.T) {
	d := newProxyDouble(t)
	d.denyFirst["example.com/gone"] = 2
	d.status["example.com/gone"] = http.StatusGone
	d.denyFirst["example.com/none"] = 2

	rows := []Row{{Module: "example.com/none"}, {Module: "example.com/gone"}}
	askProxyWith(d.proxy(), rows, 8, 0)

	for _, r := range rows {
		if !r.Unpublished {
			t.Errorf("%s: two denials did not confirm the absence: %+v", r.Module, r)
		}
		if r.Unconfirmed {
			t.Errorf("%s: confirmed twice and still unconfirmed", r.Module)
		}
	}
	// ⛔ THE STATUS IS IN THE NOTE. The first occurrence recorded none, so
	// afterwards there was no way to tell 404 ("no such module") from 410
	// ("gone") — nor an answer from a failure. The next one names itself.
	if got := rows[0].Note; !strings.Contains(got, "404") {
		t.Errorf("a 404 absence noted %q, which does not carry the status", got)
	}
	if got := rows[1].Note; !strings.Contains(got, "410") {
		t.Errorf("a 410 absence noted %q: 410 is a different claim from 404 and "+
			"the note must not flatten them", got)
	}
}

// ⛔ AN ABSENCE NOBODY COULD CONFIRM IS UNREADABLE, NEVER HOUSEKEEPING. A
// re-read that fails proves nothing about the module, so it must neither
// confirm nor contradict the first read.
func TestAnUnconfirmableAbsenceIsUnreadableRatherThanClean(t *testing.T) {
	d := newProxyDouble(t)
	d.denyFirst["example.com/a"] = 1
	d.failWith["example.com/a"] = http.StatusTooManyRequests

	rows := []Row{{Module: "example.com/a"}}
	askProxyWith(d.proxy(), rows, 8, 0)

	r := rows[0]
	if r.Unpublished {
		t.Error("a 404 the re-read could not confirm landed in the housekeeping " +
			"half of the exposure split, which is the defect")
	}
	if !r.Unconfirmed {
		t.Errorf("the row is not marked unconfirmed: %+v", r)
	}
	if !strings.Contains(r.Note, "UNCONFIRMED") {
		t.Errorf("note %q does not say the absence is unconfirmed", r.Note)
	}
}

// A read that reaches the module and fails LATER — an unparseable published
// go.mod, a version that moved — still contradicts the first read about the
// module existing, which is a different thing from confirming its floor.
func TestAReReadThatReachesTheModuleContradictsTheDenial(t *testing.T) {
	d := newProxyDouble(t)
	d.denyFirst["example.com/a"] = 1
	d.published["example.com/a"] = "not-a-version-but-parseable"

	rows := []Row{{Module: "example.com/a"}}
	askProxyWith(d.proxy(), rows, 8, 0)

	r := rows[0]
	if r.Unpublished || r.Unconfirmed {
		t.Errorf("@latest answered, so the module exists: %+v", r)
	}
	if !strings.Contains(r.Note, "published on re-read") {
		t.Errorf("note %q does not record that the denial was contradicted", r.Note)
	}
}

// The confirmation pass rewrites the row, so the fields that say WHO CAN PUSH A
// FIX have to survive it: a row that lost its repository would be reported as
// a module found in no work tree, which is a different finding.
func TestARetriedRowKeepsTheIdentityThatSaysWhoCanFixIt(t *testing.T) {
	d := newProxyDouble(t)
	d.denyFirst["example.com/a"] = 1

	want := Row{
		Module:    "example.com/a",
		Repo:      Repo{Owner: "go-fleettools", Name: "a"},
		Local:     Floors{Module: "example.com/a", Go: "1.27.4"},
		LocalFile: "/somewhere/a/go.mod",
	}
	rows := []Row{want}
	askProxyWith(d.proxy(), rows, 8, 0)

	got := rows[0]
	if got.Repo != want.Repo || got.Local != want.Local || got.LocalFile != want.LocalFile {
		t.Errorf("the re-read dropped the row's identity:\n got %+v\nwant %+v", got, want)
	}
}

// ⛔ THE CLASSIFICATION MUST NOT BE THE NOTE'S TEXT. Five rules used to
// discriminate on `Note == "unpublished"` or `Contains(Note, "UNRELEASED")`, so
// the moment this fix wrote "unpublished (404)" every one of them would have
// stopped matching and the rows would have moved into the campaign for a reason
// nobody wrote down. This test fails if prose is ever read as a verdict again.
func TestProseIsNotAVerdict(t *testing.T) {
	rows := []Row{
		// Says "unpublished" and is NOT: a consumer can install it.
		{Module: "example.com/liar", Verdict: Below, Note: "unpublished"},
		// Says "UNRELEASED" and is not that either.
		{Module: "example.com/other", Verdict: Below, Note: "fixed locally, UNRELEASED"},
	}
	pub, unpub, unreleased := exposure(rows)
	if pub != 2 || unpub != 0 {
		t.Errorf("published = %d, unpublished = %d; want 2 and 0 — the note's TEXT "+
			"decided the split, so adding a word to a note changes a verdict", pub, unpub)
	}
	if unreleased != 0 {
		t.Errorf("fixedUnreleased = %d, want 0 — read from the note's prose", unreleased)
	}

	keys, count := byDirective([]Row{
		{Module: "example.com/liar", Published: Floors{Go: "1.27.1"},
			Local: Floors{Go: "1.22"}, Verdict: Below, Note: "unpublished"},
	}, false)
	if len(keys) != 1 || keys[0] != "1.27.1" || count["1.27.1"] != 1 {
		t.Errorf("byDirective grouped on the note rather than the field: %v %v", keys, count)
	}
}

// ⛔ AND THE RULE IS WIRED UP END TO END, which is the only test that can see
// it: a confirmation pass that nothing calls, or an unreadable row the exit
// status ignores, would pass every unit test above.
func TestAnUnconfirmedRowStopsTheWholeRunRatherThanBeingCounted(t *testing.T) {
	d := newProxyDouble(t)
	d.denyFirst["github.com/go-fleettools/a"] = 1
	d.failWith["github.com/go-fleettools/a"] = http.StatusTooManyRequests

	swap(t, &sweepProxy, d.proxy)
	swap(t, &confirmPause, 0)

	root := t.TempDir()
	fakeRepo(t, root, "go-fleettools/a")
	write(t, filepath.Join(root, "go.mod"), "module github.com/go-fleettools/a\n\ngo 1.27.1\n")

	opt := offlineOpts(root, "1.27.4", nil)
	opt.offline = false
	code, _, err := reportModules(io.Discard, io.Discard, opt)
	if code != 2 {
		t.Errorf("exit = %d, want 2: an absence nobody could confirm is a "+
			"measurement that failed, not a module nobody can install", code)
	}
	if err == nil {
		t.Fatal("the run reported success")
	}
	for _, want := range []string{"github.com/go-fleettools/a", "never read"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

// ⛔⛔ THE LARGER HALF, AND IT WAS MEASURED RATHER THAN IMAGINED. A sweep of
// 1229 modules on 2026-11-15 in which EVERY proxy read failed with "no route
// to host" reported:
//
//	1229 modules against floor 1.27.4:  no-go=1229
//	⛔ 1229 modules declare a floor below 1.27.4 …
//	   they live in 1091 repositories, so the campaign is about 1091 pull requests
//	   of those, 1229 are PUBLISHED on the proxy (a consumer can install them)
//
// and exited 1. Not one byte had been read, and the proxy answered 200 for the
// first module on that list two minutes later. Published stayed blank, Judge
// read the blank as `no-go`, and `no-go` is an exposure: a complete campaign
// out of total silence, which is the costliest shape this fleet knows — a
// coherent, plausible, false story that nothing flags.
func TestAProxyThatAnswersNothingProducesNoCampaign(t *testing.T) {
	d := newProxyDouble(t)
	d.failWith["github.com/go-fleettools/a"] = http.StatusServiceUnavailable

	swap(t, &sweepProxy, d.proxy)
	swap(t, &confirmPause, 0)

	root := t.TempDir()
	fakeRepo(t, root, "go-fleettools/a")
	write(t, filepath.Join(root, "go.mod"), "module github.com/go-fleettools/a\n\ngo 1.27.1\n")

	opt := offlineOpts(root, "1.27.4", nil)
	opt.offline = false
	var out strings.Builder
	code, _, err := reportModules(&out, io.Discard, opt)

	if code != 2 {
		t.Errorf("exit = %d, want 2: the published floor was never read, so this is "+
			"\"could not look\" and not \"found a problem\"", code)
	}
	if err == nil {
		t.Fatal("a run that read nothing reported a verdict")
	}
	// ⛔ AND IT MUST NOT HAVE COSTED A CAMPAIGN. The sentence naming pull
	// requests is the one a human acts on, so its absence is the assertion.
	if strings.Contains(out.String(), "pull request") {
		t.Errorf("the report costed a campaign out of a floor it never read:\n%s", out.String())
	}
	if strings.Contains(out.String(), "PUBLISHED on the proxy") {
		t.Errorf("the report called an unread module installable:\n%s", out.String())
	}
	if !strings.Contains(out.String(), string(Unreadable)) {
		t.Errorf("the row is not marked unreadable:\n%s", out.String())
	}
}

// --offline reads no proxy at all, so there is no published floor to be
// missing: the refusal above must not fire and turn the offline mode — which
// judges working trees on purpose — into a tool that always exits 2.
func TestOfflineIsNotRefusedForAFloorItNeverMeantToRead(t *testing.T) {
	root := t.TempDir()
	fakeRepo(t, root, "go-fleettools/a")
	write(t, filepath.Join(root, "go.mod"), "module github.com/go-fleettools/a\n\ngo 1.27.1\n")

	code, _, err := reportModules(io.Discard, io.Discard, offlineOpts(root, "1.27.4", nil))
	if err != nil {
		t.Fatal(err)
	}
	if code != 1 {
		t.Errorf("offline exit = %d, want 1 (an exposure in the working tree)", code)
	}
}

// A `--repos` list feeds gopin and a baseline becomes the campaign's record,
// so neither may be written from a floor nobody read: the gap would travel
// into another tool with no note attached to it.
func TestNoListOrBaselineIsWrittenFromAFloorNobodyRead(t *testing.T) {
	d := newProxyDouble(t)
	d.failWith["github.com/go-fleettools/a"] = http.StatusServiceUnavailable

	swap(t, &sweepProxy, d.proxy)
	swap(t, &confirmPause, 0)

	root := t.TempDir()
	fakeRepo(t, root, "go-fleettools/a")
	write(t, filepath.Join(root, "go.mod"), "module github.com/go-fleettools/a\n\ngo 1.27.1\n")

	base := filepath.Join(t.TempDir(), "floor.txt")
	for _, tc := range []struct {
		name string
		set  func(*options)
	}{
		{"--repos", func(o *options) { o.repos = true }},
		{"--write-baseline", func(o *options) { o.writeBaseline = base }},
	} {
		opt := offlineOpts(root, "1.27.4", nil)
		opt.offline = false
		tc.set(&opt)
		var out strings.Builder
		code, _, err := reportModules(&out, io.Discard, opt)
		if code != 2 || err == nil {
			t.Errorf("%s: exit = %d, err = %v; want 2 and a refusal", tc.name, code, err)
		}
		if out.Len() != 0 {
			t.Errorf("%s wrote %q from a floor it never read", tc.name, out.String())
		}
	}
	if _, err := os.Stat(base); err == nil {
		t.Error("--write-baseline recorded a campaign from a floor nobody read")
	}
}

// A sweep can hold hundreds of them, and the message still has to fit a
// terminal: the names are capped and the COUNT is the finding.
func TestTheUnconfirmedListIsCappedAndSaysHowManyItHid(t *testing.T) {
	var rows []Row
	for i := 0; i < 14; i++ {
		rows = append(rows, Row{Module: fmt.Sprintf("example.com/m%d", i), Unconfirmed: true})
	}
	got := unreadModules(rows)
	if len(got) != 11 {
		t.Fatalf("named %d entries, want 10 modules plus one summary: %v", len(got), got)
	}
	if got[10] != "and 4 more" {
		t.Errorf("last entry = %q, want \"and 4 more\"", got[10])
	}
	if n := len(unreadModules([]Row{{Module: "a", ProxyRead: true}})); n != 0 {
		t.Errorf("a row that WAS read was named as unread (%d entries)", n)
	}
}
