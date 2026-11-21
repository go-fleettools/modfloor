package main

import (
	"net/http"
	"strings"
	"testing"
)

// ⛔ THE DEFECT, AS A TABLE. #16: the tool read the proxy's @latest endpoint
// while a consumer resolves the highest version in the LIST, and the two are
// inconsistent for a while after a release. Each row below is a separate way to
// get the version wrong, and the last two are the ones that produce a FALSE
// CLEAN rather than noise.
func TestTheVersionAConsumerResolvesIsTheHighestInTheList(t *testing.T) {
	for _, c := range []struct {
		name string
		list []string
		want string
	}{{
		name: "the highest release wins",
		list: []string{"v0.1.0", "v0.3.0", "v0.2.0"},
		want: "v0.3.0",
	}, {
		// ⛔ THE MEASURED CASE. The proxy answered this exact list for
		// go-fleettools/unbounded on 2026-11-15 while @latest still said
		// v0.2.0, and the tool reported the v0.2.0 floor as the published one.
		name: "the list order is not the version order",
		list: []string{"v0.3.0", "v0.1.0", "v0.2.0"},
		want: "v0.3.0",
	}, {
		// ⛔ COMPARED AS SEMVER, NEVER AS TEXT. v0.9.0 sorts above v0.10.0 as
		// strings, and this fleet crosses that boundary constantly. Sorting as
		// text here picks an OLDER release, whose floor may be fine while the
		// newer one is below: a false `ok`.
		name: "ten is above nine",
		list: []string{"v0.9.0", "v0.10.0"},
		want: "v0.10.0",
	}, {
		name: "a release outranks a higher prerelease",
		list: []string{"v1.0.0", "v1.1.0-rc.1"},
		want: "v1.0.0",
	}, {
		name: "a prerelease is used when there is no release",
		list: []string{"v1.1.0-rc.1", "v1.1.0-rc.2"},
		want: "v1.1.0-rc.2",
	}, {
		// The reference states it: "we even prefer a compatible pre-release
		// over an incompatible release" (query.go:604).
		name: "a compatible prerelease outranks an incompatible release",
		list: []string{"v2.0.0+incompatible", "v1.1.0-rc.1"},
		want: "v1.1.0-rc.1",
	}, {
		name: "an incompatible release is used when nothing else is there",
		list: []string{"v2.0.0+incompatible", "v3.0.0+incompatible"},
		want: "v3.0.0+incompatible",
	}, {
		// ⛔ semver.IsValid ACCEPTS "v1.0", and it compares EQUAL to v1.0.0.
		// Picking it would request @v/v1.0.mod and manufacture a 404 out of a
		// well-formed list, so only canonical versions are considered.
		name: "a non-canonical entry is not a version",
		list: []string{"v1.0", "v0.9.0"},
		want: "v0.9.0",
	}, {
		name: "a line that is not semver is ignored rather than trusted",
		list: []string{"latest", "main", "v0.1.0", ""},
		want: "v0.1.0",
	}} {
		t.Run(c.name, func(t *testing.T) {
			got, ok := pickVersion(c.list)
			if !ok {
				t.Fatalf("pickVersion(%q) found nothing, want %q", c.list, c.want)
			}
			if got != c.want {
				t.Errorf("pickVersion(%q) = %q, want %q", c.list, got, c.want)
			}
		})
	}
}

// A list holding nothing usable is not a version, and must not be invented.
func TestAListWithNoUsableVersionResolvesNothing(t *testing.T) {
	for _, list := range [][]string{nil, {}, {""}, {"main", "v1.0", "nonsense"}} {
		if got, ok := pickVersion(list); ok {
			t.Errorf("pickVersion(%q) = %q, want no version", list, got)
		}
	}
}

// ⛔ THE FALLBACK IS THE REFERENCE'S, AND ONLY IN THE REFERENCE'S CASE. The go
// command consults the @latest endpoint only when the version list yields
// nothing — an untagged module. Reaching for it any earlier is the defect.
func TestTheLatestEndpointAnswersOnlyForAModuleWithNoTag(t *testing.T) {
	d := newProxyDouble(t)
	d.versions["example.com/untagged"] = []string{}

	ver, from, err := d.proxy().Resolve(t.Context(), "example.com/untagged")
	if err != nil {
		t.Fatalf("Resolve on an untagged module: %v", err)
	}
	if ver != "v1.2.3" || from != FromLatest {
		t.Errorf("resolved %q from %q, want v1.2.3 from the @latest fallback", ver, from)
	}
	if d.latestReads["example.com/untagged"] != 1 {
		t.Errorf("the @latest endpoint was read %d times, want 1",
			d.latestReads["example.com/untagged"])
	}
}

// ⛔ AND IT IS NOT READ OTHERWISE — which is a COST claim, not a style one. One
// extra request per module over the 834 published modules of this fleet is a
// third more load on a public proxy for a diagnostic the resolution no longer
// needs. A test that only checked the version would pass while paying it.
func TestAModuleWithTagsCostsNoReadOfTheLatestEndpoint(t *testing.T) {
	d := newProxyDouble(t)
	d.versions["example.com/tagged"] = []string{"v0.1.0", "v0.2.0"}

	ver, from, err := d.proxy().Resolve(t.Context(), "example.com/tagged")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if ver != "v0.2.0" || from != FromList {
		t.Errorf("resolved %q from %q, want v0.2.0 from the list", ver, from)
	}
	if n := d.latestReads["example.com/tagged"]; n != 0 {
		t.Errorf("the @latest endpoint was read %d times for a tagged module, want 0", n)
	}
}

// ⛔ A FAILED FALLBACK READ IS NOT A VERSION OF "". The list can be
// legitimately empty — an untagged module — and the @latest read that follows
// it can then fail. Without the error check on that read, Resolve returns an
// empty version with a NIL error: the caller is told the module resolved, to
// nothing. That is `unread-answer-reads-as-a-negative` inside the fix for #16.
//
// Found by mutsweep, which reported this line as the one survivor of the seven
// refusals in resolve.go; the gate that refuses the push is what asked for this
// test to exist.
func TestAFailedFallbackReadIsAnErrorAndNotAnEmptyVersion(t *testing.T) {
	d := newProxyDouble(t)
	d.versions["example.com/untagged"] = []string{}
	d.latestFailWith["example.com/untagged"] = http.StatusInternalServerError

	ver, from, err := d.proxy().Resolve(t.Context(), "example.com/untagged")
	if err == nil {
		t.Fatalf("a failed @latest read resolved to %q from %q with no error", ver, from)
	}
	if ver != "" {
		t.Errorf("a failed read produced the version %q", ver)
	}
}

// ⛔ AN EMPTY LIST IS AN ANSWER, A DENIED LIST IS NOT. 200 with an empty body
// is a published module with no tag; 404 is an absence. Collapsing the two is
// `unread-answer-reads-as-a-negative`, the shape of #17.
func TestAnEmptyListIsNotAnAbsence(t *testing.T) {
	d := newProxyDouble(t)
	d.versions["example.com/empty"] = []string{}

	vers, err := d.proxy().List(t.Context(), "example.com/empty")
	if err != nil {
		t.Fatalf("an empty list came back as an error: %v", err)
	}
	if len(vers) != 0 {
		t.Errorf("List = %q, want no versions", vers)
	}

	d.denyFirst["example.com/gone"] = 1
	d.status["example.com/gone"] = http.StatusGone
	if _, err := d.proxy().List(t.Context(), "example.com/gone"); err == nil {
		t.Error("a 410 on the list was not reported as an absence")
	} else if unpublishedStatus(err) != http.StatusGone {
		t.Errorf("the absence did not carry its status: %v", err)
	}
}

// The list endpoint takes a module path straight into a URL, so it is checked
// like every other one: the path comes from a go.mod found by walking a disk.
func TestTheListEndpointRefusesAPathItCannotPutInAURL(t *testing.T) {
	d := newProxyDouble(t)
	for _, bad := range []string{"../../../etc/passwd", "", "a//b", "a/../b"} {
		if _, err := d.proxy().List(t.Context(), bad); err == nil {
			t.Errorf("List(%q) was accepted", bad)
		}
	}
}

// ⛔ THE COLUMN MUST NOT BE NAMED AFTER AN ENDPOINT IT NO LONGER READS. The
// header said @LATEST while the number under it comes from the version list;
// a reader who trusts the header draws the wrong conclusion about a module
// whose two endpoints disagree.
func TestTheColumnIsNamedForWhatItHoldsNotForAnEndpoint(t *testing.T) {
	var b strings.Builder
	printModules(&b, []Row{{
		Module:   "example.com/a",
		Resolved: "v0.3.0",
		From:     FromList,
	}}, options{}, Census{})
	out := b.String()
	if !strings.Contains(out, "RESOLVED") {
		t.Errorf("the header does not name the column RESOLVED:\n%s", out)
	}
	if strings.Contains(out, "@LATEST") {
		t.Errorf("the header still names the @latest endpoint:\n%s", out)
	}
	if !strings.Contains(out, "v0.3.0") {
		t.Errorf("the resolved version is not printed:\n%s", out)
	}
	if strings.Contains(out, "(@latest)") {
		t.Errorf("a version resolved from the list was marked as the fallback:\n%s", out)
	}
}

// A row resolved through the fallback says so, because "this module has no tag"
// is a different fact from "this module's latest release is v1.2.3".
func TestARowResolvedThroughTheFallbackSaysSo(t *testing.T) {
	var b strings.Builder
	printModules(&b, []Row{{
		Module:   "example.com/untagged",
		Resolved: "v0.0.0-20261121120000-abcdefabcdef",
		From:     FromLatest,
	}}, options{}, Census{})
	if !strings.Contains(b.String(), "(@latest)") {
		t.Errorf("the fallback is not reported:\n%s", b.String())
	}
}
