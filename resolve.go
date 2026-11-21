package main

import (
	"context"
	"fmt"
	"strings"

	"golang.org/x/mod/semver"
)

// An Endpoint names which of the proxy's two version endpoints decided a row.
//
// It is reported rather than kept internal because the two do not always agree,
// and a reader chasing "but I just released that" needs to know which one
// answered.
type Endpoint string

const (
	// FromList is $module/@v/list, the enumeration a consumer resolves from.
	FromList Endpoint = "list"
	// FromLatest is $module/@latest, consulted only when the list is empty.
	FromLatest Endpoint = "@latest"
)

// List reports the versions the proxy's @v/list endpoint offers, one per line,
// in the order the proxy gave them.
//
// ⛔ AN EMPTY LIST IS AN ANSWER, NOT AN ABSENCE. A module with no tag is
// published and reachable; its list is legitimately empty and comes back as an
// empty slice with a nil error. Reading that as "unpublished" would be
// `unread-answer-reads-as-a-negative` in a new place — the defect #17 was.
func (p *Proxy) List(ctx context.Context, module string) ([]string, error) {
	if !ValidModulePath(module) {
		return nil, fmt.Errorf("%q: %w", module, ErrBadModulePath)
	}
	body, err := p.get(ctx, module+"/@v/list")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, line := range strings.Split(string(body), "\n") {
		if v := strings.TrimSpace(line); v != "" {
			out = append(out, v)
		}
	}
	return out, nil
}

// Resolve reports the version `go install <module>@latest` would actually
// choose, and which endpoint decided it.
//
// ⛔ THAT IS NOT THE @latest ENDPOINT, AND THE DIFFERENCE IS THIS TOOL'S WHOLE
// CLAIM. The go command resolves the query "latest" by LISTING versions and
// taking the highest; it falls back to the @latest endpoint only when the list
// yields nothing. The two endpoints are inconsistent for a while after a
// release — measured 2026-11-15 on five of eight modules released minutes
// earlier, where @v/list already held v0.3.0 while @latest still answered
// v0.2.0 (#16).
//
// Read from the reference implementation rather than inferred, go1.27.4
// src/cmd/go/internal/modload/query.go:353-367 — releases[len-1], else
// prereleases[len-1], else repo.Latest() behind qm.mayUseLatest.
//
// ⛔ The direction that was MEASURED is the harmless one: a stale @latest named
// an older release and produced a false BELOW, which is noise. The same
// mechanism produces a false `ok` whenever @latest names a release whose floor
// is fine while a newer, lower-floor release exists — a false clean out of a
// tool whose exit status gates a campaign, which is the failure this tool was
// written to prevent.
func (p *Proxy) Resolve(ctx context.Context, module string) (string, Endpoint, error) {
	vers, err := p.List(ctx, module)
	if err != nil {
		return "", "", err
	}
	if v, ok := pickVersion(vers); ok {
		return v, FromList, nil
	}
	// No usable tag in the list: this is what the @latest endpoint exists for,
	// and what the go command falls back to.
	v, err := p.Latest(ctx, module)
	if err != nil {
		return "", "", err
	}
	return v, FromLatest, nil
}

// pickVersion chooses, from the proxy's version list, the version a consumer's
// @latest resolves to. It reports false when the list holds nothing usable.
//
// The order is the reference implementation's, and each clause is a separate
// way to get a wrong answer:
//
//   - a canonical RELEASE outranks every prerelease (query.go:361-366);
//   - among equals, the SEMVER maximum, never the textual one — v0.10.0 is
//     ahead of v0.9.0 as semver and behind it as text, and this fleet's tags
//     cross that boundary constantly;
//   - a +incompatible version loses to any compatible one, including to a
//     compatible PRERELEASE, which the reference states explicitly: "we even
//     prefer a compatible pre-release over an incompatible release"
//     (query.go:604);
//   - a line that is not valid CANONICAL semver is ignored rather than
//     trusted. The list is data from the network, and semver.IsValid alone is
//     too weak: it accepts "v1.0", which would then be compared as equal to
//     "v1.0.0", chosen, and requested as @v/v1.0.mod — a 404 manufactured out
//     of a well-formed list.
//
// ⚠ ONE STATED DIVERGENCE from the reference, and the argument that it cannot
// change a verdict here. The reference prefers a +incompatible version when no
// compatible version has a go.mod file (query.go:598-621); this function always
// prefers the compatible one. A +incompatible version is, by construction, a
// major-version tag published WITHOUT a go.mod, so neither candidate in that
// case carries a `go` directive at all and the row reads `no-go` whichever is
// chosen. The divergence is therefore unreachable for this tool's verdict,
// which is why it is documented instead of implemented.
//
// That "by construction" is the reference's own statement and not an
// inference: modfetch/coderepo.go:399 — "If the version is +incompatible, then
// the go.mod file must not exist: +incompatible is not an ongoing opt-out from
// semantic import versioning" — and :1025, "+incompatible versions necessarily
// do not [have a go.mod file]".
func pickVersion(vers []string) (string, bool) {
	var release, prerelease, incompatible string
	better := func(cur, v string) string {
		if cur == "" || semver.Compare(v, cur) > 0 {
			return v
		}
		return cur
	}
	for _, v := range vers {
		// The +incompatible suffix is build metadata, which Canonical strips,
		// so the shape is checked on the version WITHOUT it.
		base := strings.TrimSuffix(v, "+incompatible")
		if !semver.IsValid(base) || base != semver.Canonical(base) {
			continue
		}
		switch {
		case strings.HasSuffix(v, "+incompatible"):
			incompatible = better(incompatible, v)
		case semver.Prerelease(v) != "":
			prerelease = better(prerelease, v)
		default:
			release = better(release, v)
		}
	}
	for _, v := range []string{release, prerelease, incompatible} {
		if v != "" {
			return v, true
		}
	}
	return "", false
}
