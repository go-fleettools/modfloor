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
)

// ErrUnpublished is returned when the proxy has no such module or version.
// It is a distinct error because "never released" and "released with a bad
// floor" are different states and only one of them is a defect.
var ErrUnpublished = errors.New("not published")

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

// Latest reports the version @latest resolves to.
func (p *Proxy) Latest(ctx context.Context, module string) (string, error) {
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
		return nil, ErrUnpublished
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
