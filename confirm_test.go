package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
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
	reads    map[string]int
}

func newProxyDouble(t *testing.T) *proxyDouble {
	t.Helper()
	d := &proxyDouble{
		denyFirst: map[string]int{},
		status:    map[string]int{},
		published: map[string]string{},
		failWith:  map[string]int{},
		reads:     map[string]int{},
	}
	d.srv = httptest.NewServer(http.HandlerFunc(d.serve))
	t.Cleanup(d.srv.Close)
	return d
}

func (d *proxyDouble) proxy() *Proxy {
	return &Proxy{Base: d.srv.URL, Client: d.srv.Client()}
}

func (d *proxyDouble) serve(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/")
	switch {
	case strings.HasSuffix(path, "/@latest"):
		mod := strings.TrimSuffix(path, "/@latest")
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
	if r.Published.Go != "1.27.1" || r.Latest != "v1.2.3" || !r.ProxyRead {
		t.Errorf("the published floor was not read: %+v", r)
	}
	if d.reads["example.com/a"] != 2 {
		t.Errorf("@latest read %d times, want 2 (the first and its confirmation)",
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
	for _, want := range []string{"github.com/go-fleettools/a", "UNKNOWN"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

// A sweep can hold hundreds of them, and the message still has to fit a
// terminal: the names are capped and the COUNT is the finding.
func TestTheUnconfirmedListIsCappedAndSaysHowManyItHid(t *testing.T) {
	var rows []Row
	for i := 0; i < 14; i++ {
		rows = append(rows, Row{Module: fmt.Sprintf("example.com/m%d", i), Unconfirmed: true})
	}
	got := unconfirmedModules(rows)
	if len(got) != 11 {
		t.Fatalf("named %d entries, want 10 modules plus one summary: %v", len(got), got)
	}
	if got[10] != "and 4 more" {
		t.Errorf("last entry = %q, want \"and 4 more\"", got[10])
	}
	if n := len(unconfirmedModules([]Row{{Module: "a"}})); n != 0 {
		t.Errorf("a confirmed row was named as unconfirmed (%d entries)", n)
	}
}
