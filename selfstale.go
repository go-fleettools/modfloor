package main

import (
	"context"
	"fmt"
	"io"
	"runtime/debug"
	"time"

	"golang.org/x/mod/semver"
)

// ⛔ WHY THIS EXISTS, AND IT IS THIS TOOL'S OWN SUBJECT TURNED ON ITSELF.
//
// modfloor's whole claim is that a measurement must be taken from what is
// PUBLISHED rather than from whatever happens to be on the machine. On
// 2026-11-15 a session published two issues against this repository from a
// binary six minor releases behind:
//
//	go version -m ~/.local/bin/modfloor  ->  v0.3.0   (v0.9.0 was released)
//
// v0.3.0 predates the exposure split (#6, #9, #11), so its report stopped after
// the "N modules declare a floor below" line. That absence was read as "the
// summary conflates consumer exposure with housekeeping" and filed as #17 —
// which was closed the same hour as an artefact of the stale binary, after a
// re-run with v0.9.0 answered 403 PUBLISHED of 498 instead of almost none.
//
// ⛔ The fleet already warns about exactly this, and that is the galling part:
// every command hosted in go-fleettools/fleettools prints a staleness note on
// stderr through its shared helper. A tool that lives in its OWN repository
// gets no such note, so this repository had no way of saying it was old. Hence
// a self-check that needs nothing new: the proxy client is already here, and
// the version is in the binary's own build info.
//
// ⚠ IT WARNS AND NEVER FAILS. A measurement taken with an old binary is still
// a measurement; what must not happen is PUBLISHING it without knowing. A hard
// failure would also make every run depend on the network, which is the
// opposite of this tool's "needs no credential, costs nothing" property.

// staleNote is the one line printed when this binary is behind its own module's
// newest release. Its shape follows the fleet's other notes: "note: " then what
// moved, so a reader of a transcript can see it without it looking like an
// error.
const staleNote = "note: this is modfloor %s, but %s is published — a report from an old binary can lack a whole column (see the comment in selfstale.go). Reinstall: GOBIN=~/.local/bin go install %s@%s\n"

// selfBuild reports the module path and version this binary was built from.
//
// A build from source rather than from a tag has version "(devel)" or empty,
// and there is nothing to compare it against: a developer running their own
// tree is not stale, they are ahead.
func selfBuild() (path, version string, ok bool) {
	bi, read := debug.ReadBuildInfo()
	if !read || bi == nil {
		return "", "", false
	}
	p, v := bi.Main.Path, bi.Main.Version
	if p == "" || v == "" || v == "(devel)" {
		return "", "", false
	}
	return p, v, true
}

// staleSelf is selfBuild behind a variable so that the test of main's WIRING
// can supply a released identity: under `go test` the main module's version is
// "(devel)", so the real selfBuild correctly declines and no note could ever
// appear — which would leave the wiring unobservable.
var staleSelf = selfBuild

// WarnIfStale prints one line on w when a newer release of this binary's own
// module is published. It is quiet about everything else, including its own
// failures: the network being down must not add noise to a report.
func WarnIfStale(w io.Writer, p *Proxy, timeout time.Duration) {
	path, version, ok := staleSelf()
	if !ok {
		return
	}
	warnIfStale(w, p, path, version, timeout)
}

// warnIfStale is the testable half: the caller supplies the identity, so a test
// does not have to fake this binary's build info.
func warnIfStale(w io.Writer, p *Proxy, module, version string, timeout time.Duration) {
	// A version that is not semver cannot be compared, and guessing is worse
	// than silence.
	if !semver.IsValid(version) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	latest, err := p.Latest(ctx, module)
	if err != nil || !semver.IsValid(latest) {
		return
	}
	// ⛔ COMPARED AS SEMVER, NEVER AS TEXT: v0.10.0 is ahead of v0.9.0 and
	// behind it as strings, and this fleet's tags cross that boundary
	// constantly (prwait went v0.9.0 -> v0.10.0 the same day this was written).
	if semver.Compare(version, latest) >= 0 {
		return
	}
	fmt.Fprintf(w, staleNote, version, latest, module, latest)
}
