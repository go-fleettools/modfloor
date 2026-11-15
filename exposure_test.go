package main

import "testing"

// ⛔ The split exists because one number over both populations cannot be
// prioritised: hundreds of modules below the floor reads as an emergency until
// somebody says how many a consumer can actually install. (The figure first
// written here, 935 of 1025, counted 540 modules other projects publish; see
// Repo.Declares.)
//
// Both halves, and the middle: a published exposure must be counted as the
// campaign, an unpublished one as housekeeping, and an `ok` module as neither.
func TestTheExposureSplitSeparatesWhatAConsumerCanReach(t *testing.T) {
	rows := []Row{
		// The campaign: on the proxy, and below the floor.
		{Module: "example.com/a", Verdict: Below},
		{Module: "example.com/b", Verdict: NoFloor},
		// Housekeeping: nobody can install these, so only a local build
		// honours their floor.
		// The classification is the FIELD. These rows carry a note too,
		// spelling the status that said so, and the note's text is read by
		// nobody: it used to be the discriminator, and that made #17's fix
		// — which adds "(404)" to it — a silent reclassification of every
		// one of these rows into the campaign.
		{Module: "example.com/c", Verdict: Below, Unpublished: true, Note: "unpublished (404, re-read 404)"},
		{Module: "example.com/d", Verdict: NoFloor, Unpublished: true, Note: "unpublished (410, re-read 410)"},
		{Module: "example.com/e", Verdict: Below, Unpublished: true, Note: "unpublished (404, re-read 404)"},
		// Neither: already at or above the floor.
		{Module: "example.com/f", Verdict: OK},
		// Nor this: it FAILS CLOSED under GOTOOLCHAIN=local rather than
		// building vulnerable code, so it is not an exposure at all.
		{Module: "example.com/g", Verdict: Pinned},
	}
	pub, unpub, unreleased := exposure(rows)
	if pub != 2 {
		t.Errorf("published exposures = %d, want 2", pub)
	}
	if unpub != 3 {
		t.Errorf("unpublished exposures = %d, want 3", unpub)
	}
	if unreleased != 0 {
		t.Errorf("fixedUnreleased = %d, want 0", unreleased)
	}

	// The sum must be the whole exposure and nothing else: a row that drifted
	// out of both buckets would shrink the campaign silently.
	exposed := 0
	for _, r := range rows {
		if r.Verdict.Exposed() {
			exposed++
		}
	}
	if pub+unpub != exposed {
		t.Errorf("%d published + %d unpublished = %d, but %d rows are exposed",
			pub, unpub, pub+unpub, exposed)
	}
}

// "fixed locally, UNRELEASED" is the state a campaign most needs to see: the
// pull request is merged and the work is NOT finished, because the consumer
// reads the published go.mod and not the branch.
func TestAFixThatWasNeverReleasedIsCountedApart(t *testing.T) {
	rows := []Row{
		{Module: "example.com/a", Verdict: Below, Unreleased: true, Note: "fixed locally, UNRELEASED"},
		{Module: "example.com/b", Verdict: Below},
	}
	pub, unpub, unreleased := exposure(rows)
	if unreleased != 1 {
		t.Errorf("fixedUnreleased = %d, want 1", unreleased)
	}
	// It is STILL an exposure: the published floor is what a consumer honours,
	// so an unreleased fix changes nothing for them yet.
	if pub != 2 || unpub != 0 {
		t.Errorf("published = %d, unpublished = %d; want 2 and 0 — an unreleased "+
			"fix does not stop the published floor being the exposure", pub, unpub)
	}
}

// An empty report must not report a clean split: zero and zero is also what a
// measurement that read nothing would print, and the caller only reaches this
// when something was condemned.
func TestAnEmptyReportSplitsToZeroWithoutClaimingAnything(t *testing.T) {
	pub, unpub, unreleased := exposure(nil)
	if pub != 0 || unpub != 0 || unreleased != 0 {
		t.Errorf("exposure(nil) = %d, %d, %d; want zeros", pub, unpub, unreleased)
	}
}
