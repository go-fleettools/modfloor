package main

import (
	"strings"
	"testing"
)

// ⛔ THE CAMPAIGN IS NOT ONE RUN OF ONE TOOL. `gopin --gomod --from X` names the
// version to move EXACTLY — deliberately, because "move anything older" cannot
// tell a stale pin from a deliberate hold — so a campaign over modules declaring
// several floors is one run per floor. "221 pull requests" is the cost of the
// work; this is the shape of it, and only the shape can be scheduled.
func TestTheExposureIsGroupedByTheFloorEachModuleDeclares(t *testing.T) {
	rows := []Row{
		{Module: "a", Published: Floors{Go: "1.27.2"}, Verdict: Below},
		{Module: "b", Published: Floors{Go: "1.27.2"}, Verdict: Below},
		{Module: "c", Published: Floors{Go: "1.27.1"}, Verdict: Below},
		// No directive at all: `--from` has nothing to match, so it needs a
		// different act and is grouped apart.
		{Module: "d", Verdict: NoFloor},
		// Judged on the WORKING TREE because it was never published, so that
		// is the floor a campaign would have to name.
		{Module: "e", Local: Floors{Go: "1.22"}, Verdict: Below, Note: "unpublished"},
		// Neither of these is work.
		{Module: "f", Published: Floors{Go: "1.27.3"}, Verdict: OK},
		{Module: "g", Published: Floors{Go: "1.26.0"}, Verdict: Pinned},
	}
	keys, count := byDirective(rows, false)

	// Newest first, so the floors a security release makes urgent are read
	// first, and compared as Go versions rather than as text.
	if strings.Join(keys, " ") != "1.27.2 1.27.1 1.22 (none)" {
		t.Fatalf("keys = %v", keys)
	}
	if count["1.27.2"] != 2 || count["1.27.1"] != 1 || count["1.22"] != 1 || count["(none)"] != 1 {
		t.Fatalf("counts = %v", count)
	}
	// The sum must be the whole exposure: a module drifting out of every group
	// would shrink the campaign silently.
	total := 0
	for _, n := range count {
		total += n
	}
	exposed := 0
	for _, r := range rows {
		if r.Verdict.Exposed() {
			exposed++
		}
	}
	if total != exposed {
		t.Errorf("%d grouped but %d exposed", total, exposed)
	}
}

// 1.9 is below 1.10, which text order gets wrong, and this list is read by
// somebody deciding which run to do first.
func TestTheFloorsAreOrderedAsVersionsNotAsText(t *testing.T) {
	rows := []Row{
		{Module: "a", Published: Floors{Go: "1.9"}, Verdict: Below},
		{Module: "b", Published: Floors{Go: "1.10"}, Verdict: Below},
		{Module: "c", Published: Floors{Go: "1.27.10"}, Verdict: Below},
		{Module: "d", Published: Floors{Go: "1.27.2"}, Verdict: Below},
	}
	keys, _ := byDirective(rows, false)
	if strings.Join(keys, " ") != "1.27.10 1.27.2 1.10 1.9" {
		t.Fatalf("keys = %v", keys)
	}
}

// And it reaches the report, where somebody deciding whether to run the
// campaign is standing.
func TestTheReportNamesEachFromValue(t *testing.T) {
	root := t.TempDir()
	fakeRepo(t, root+"/one", "go-fleettools/one")
	write(t, root+"/one/go.mod", "module github.com/go-fleettools/one\n\ngo 1.27.1\n")
	fakeRepo(t, root+"/two", "go-fleettools/two")
	write(t, root+"/two/go.mod", "module github.com/go-fleettools/two\n\ngo 1.27.2\n")

	var out strings.Builder
	if code, _, err := reportModules(&out, &out, offlineOpts(root, "1.27.3", ParseOwners("go-fleettools"))); err != nil || code != 1 {
		t.Fatalf("code=%d err=%v\n%s", code, err, out.String())
	}
	if !strings.Contains(out.String(), "they declare 2 different floors, so `gopin --gomod --from X --version 1.27.3` is 2 runs and not one:") {
		t.Error(out.String())
	}
	if !strings.Contains(out.String(), "--from 1.27.2     1 module(s)") {
		t.Error(out.String())
	}
}
