package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"golang.org/x/mod/semver"
)

// ErrUnpublished is returned when the proxy has no such module or version.
// It is a distinct error because "never released" and "released with a bad
// floor" are different states and only one of them is a defect.
var ErrUnpublished = errors.New("not published")

// An UnpublishedError is ErrUnpublished with the status that said so.
//
// ⛔ THE STATUS IS KEPT BECAUSE THE FIRST OCCURRENCE WAS UNDIAGNOSABLE WITHOUT
// IT. A fleet-scale sweep labelled published modules "unpublished" (#17) and
// the note recorded no code, so afterwards there was no way to tell a 404 ("no
// such module") from a 410 ("gone"), and no way to show the sweep had been
// answered at all rather than failing. A finding whose next occurrence cannot
// be diagnosed is half a finding, and that omission was part of the defect.
type UnpublishedError struct {
	Path   string
	Status int
}

func (e *UnpublishedError) Error() string {
	return fmt.Sprintf("%s: not published (%d)", e.Path, e.Status)
}

// Is keeps errors.Is(err, ErrUnpublished) true, so no existing caller has to
// learn about the status in order to keep working.
func (e *UnpublishedError) Is(target error) bool { return target == ErrUnpublished }

// unpublishedStatus returns the status an unpublished answer carried, or 0 if
// the error is not one. Callers use it to put the code in a note.
func unpublishedStatus(err error) int {
	var u *UnpublishedError
	if errors.As(err, &u) {
		return u.Status
	}
	return 0
}

// A Proxy asks the Go module proxy what a module publishes.
//
// The proxy is used rather than the GitHub API on purpose. It needs no
// credential, so no secret can reach a URL or a command line; it costs nothing
// against GitHub's hourly budget, which the fleet's Renovate runners depend on;
// and it answers the question a consumer actually asks, since `go install`
// reads the same endpoint.
type Proxy struct {
	Base   string // default https://proxy.golang.org
	Client *http.Client
}

func NewProxy() *Proxy {
	return &Proxy{
		Base:   "https://proxy.golang.org",
		Client: &http.Client{Timeout: 30 * time.Second},
	}
}

// ValidModulePath reports whether a string is safe to place in a proxy URL.
//
// The module path arrives from a `module` directive in a go.mod found by
// walking a disk, so it is UNTRUSTED input: a cloned repository can declare
// anything. A path holding ".." would be sent to the proxy as a traversal and,
// worse, could be rewritten by any intermediate into a request for a different
// resource. The shape is checked before it is ever concatenated into a URL.
func ValidModulePath(s string) bool {
	if s == "" || len(s) > 2048 {
		return false
	}
	if strings.ContainsAny(s, " \t\r\n\\?#%@:") || strings.Contains(s, "//") {
		return false
	}
	if strings.HasPrefix(s, "/") || strings.HasSuffix(s, "/") {
		return false
	}
	for _, seg := range strings.Split(s, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// ErrBadModulePath means the path is not one that may be put in a URL.
var ErrBadModulePath = errors.New("not a usable module path")

// List returns the published versions from @v/list, or an UnpublishedError
// if the proxy has no such module.
func (p *Proxy) List(ctx context.Context, module string) ([]string, error) {
	if !ValidModulePath(module) {
		return nil, fmt.Errorf("%q: %w", module, ErrBadModulePath)
	}
	body, err := p.get(ctx, module+"/@v/list")
	if err != nil {
		return nil, err
	}
	return strings.Fields(string(body)), nil
}

// FilterTaggedVersions returns the subset of versions that are valid semver,
// not prereleases, and not +incompatible.
func FilterTaggedVersions(versions []string) []string {
	var out []string
	for _, v := range versions {
		if !semver.IsValid(v) {
			continue
		}
		if semver.Prerelease(v) != "" {
			continue
		}
		if strings.HasSuffix(v, "+incompatible") {
			continue
		}
		out = append(out, v)
	}
	return out
}

// MaxTaggedVersion returns the highest semver among tagged release versions,
// or "" if none match.
func MaxTaggedVersion(versions []string) string {
	var max string
	for _, v := range FilterTaggedVersions(versions) {
		if max == "" || semver.Compare(v, max) > 0 {
			max = v
		}
	}
	return max
}

// ResolvedVersion is what a module's newest version resolution established.
type ResolvedVersion struct {
	Version   string // the version resolved to (semver max from @v/list, or from @latest)
	FromList  bool   // true if resolved from @v/list
	Endpoint  string // the version @latest answered, if queried
	Disagreed bool   // true when @v/list and @latest answered different versions
}

// Resolve resolves the newest version of a module the way the go command does:
// by reading @v/list, discarding prereleases and +incompatible, and taking the
// semver maximum. If the list is empty (an untagged module, or the proxy returned
// no list), it falls back to @latest.
func (p *Proxy) Resolve(ctx context.Context, module string) (ResolvedVersion, error) {
	if !ValidModulePath(module) {
		return ResolvedVersion{}, fmt.Errorf("%q: %w", module, ErrBadModulePath)
	}

	list, listErr := p.List(ctx, module)
	maxVer := ""
	if listErr == nil {
		maxVer = MaxTaggedVersion(list)
	}

	// If we got a valid maximum from @v/list, also query @latest to check if
	// the proxy's @latest cache has settled or disagrees (#16).
	if maxVer != "" {
		endpointVer, endpointErr := p.EndpointLatest(ctx, module)
		if endpointErr == nil && endpointVer != "" && endpointVer != maxVer {
			return ResolvedVersion{
				Version:   maxVer,
				FromList:  true,
				Endpoint:  endpointVer,
				Disagreed: true,
			}, nil
		}
		return ResolvedVersion{
			Version:  maxVer,
			FromList: true,
			Endpoint: endpointVer,
		}, nil
	}

	// No tagged releases in @v/list (or @v/list was absent): fall back to @latest.
	endpointVer, err := p.EndpointLatest(ctx, module)
	if err != nil {
		return ResolvedVersion{}, err
	}
	return ResolvedVersion{
		Version:  endpointVer,
		FromList: false,
		Endpoint: endpointVer,
	}, nil
}

// EndpointLatest reads the @latest endpoint directly without consulting @v/list.
func (p *Proxy) EndpointLatest(ctx context.Context, module string) (string, error) {
	if !ValidModulePath(module) {
		return "", fmt.Errorf("%q: %w", module, ErrBadModulePath)
	}
	body, err := p.get(ctx, module+"/@latest")
	if err != nil {
		return "", err
	}
	var info struct{ Version string }
	if err := json.Unmarshal(body, &info); err != nil {
		return "", fmt.Errorf("@latest for %s: %w", module, err)
	}
	if info.Version == "" {
		return "", fmt.Errorf("@latest for %s: no version in reply", module)
	}
	return info.Version, nil
}

// Latest reports the newest version of a module according to the go command's
// rules: the highest tagged release from @v/list, or @latest if the list has none.
func (p *Proxy) Latest(ctx context.Context, module string) (string, error) {
	res, err := p.Resolve(ctx, module)
	if err != nil {
		return "", err
	}
	return res.Version, nil
}

// Mod fetches one published go.mod.
func (p *Proxy) Mod(ctx context.Context, module, ver string) ([]byte, error) {
	if !ValidModulePath(module) {
		return nil, fmt.Errorf("%q: %w", module, ErrBadModulePath)
	}
	// The version comes from the proxy's own @latest reply, but it is still
	// checked: a reply is data from the network, and it ends up in a URL.
	if ver == "" || !ValidModulePath(ver) {
		return nil, fmt.Errorf("version %q: %w", ver, ErrBadModulePath)
	}
	return p.get(ctx, module+"/@v/"+ver+".mod")
}

func (p *Proxy) get(ctx context.Context, path string) ([]byte, error) {
	esc, err := escapePath(path)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.Base+"/"+esc, nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound, http.StatusGone:
		return nil, &UnpublishedError{Path: path, Status: resp.StatusCode}
	default:
		return nil, fmt.Errorf("%s: %s", path, resp.Status)
	}
	// Capped: a go.mod is kilobytes, and a proxy that answers something else
	// must not be allowed to fill memory.
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}

// escapePath applies the module proxy's case encoding: an upper-case letter is
// written as '!' followed by its lower-case form, so that the protocol works on
// case-insensitive filesystems.
//
// Implemented here rather than taken from golang.org/x/mod's module.EscapePath
// to keep this a zero-dependency module.
func escapePath(path string) (string, error) {
	if !strings.ContainsFunc(path, func(r rune) bool { return r >= 'A' && r <= 'Z' }) {
		return path, nil
	}
	var b strings.Builder
	b.Grow(len(path) + 8)
	for _, r := range path {
		if r >= 'A' && r <= 'Z' {
			b.WriteByte('!')
			b.WriteRune(r + ('a' - 'A'))
			continue
		}
		b.WriteRune(r)
	}
	return b.String(), nil
}
