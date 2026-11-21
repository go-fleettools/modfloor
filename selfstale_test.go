package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// proxyServing answers for one module with one version, and 404 for anything
// else — the shape the real proxy has.
//
// ⛔ IT SERVES BOTH VERSION ENDPOINTS, because a consumer's @latest is resolved
// from @v/list and only falls back to @latest for an untagged module (#16).
// While this double served @latest alone, the staleness check read a 404 for
// the list and went SILENT — and silence is this function's success output, so
// two tests caught it only because they assert the note's TEXT. A double that
// serves one endpoint of a two-endpoint protocol measures the wrong one.
func proxyServing(t *testing.T, module, version string) *Proxy {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/" + module + "/@v/list":
			_, _ = w.Write([]byte(version + "\n"))
		case "/" + module + "/@latest":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"Version":"` + version + `"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return &Proxy{Base: srv.URL, Client: srv.Client()}
}

func TestAnOlderBinaryIsNamedWithTheCommandThatReplacesIt(t *testing.T) {
	p := proxyServing(t, "github.com/go-fleettools/modfloor", "v0.9.0")
	var out strings.Builder
	warnIfStale(&out, p, "github.com/go-fleettools/modfloor", "v0.3.0", 5*time.Second)

	got := out.String()
	if got == "" {
		t.Fatal("a binary six releases behind printed nothing; this is the defect the file exists for")
	}
	// Both versions must appear: a note saying only "you are old" sends the
	// reader to `go version -m` to learn what they are running.
	for _, want := range []string{"v0.3.0", "v0.9.0", "go install", "github.com/go-fleettools/modfloor@v0.9.0"} {
		if !strings.Contains(got, want) {
			t.Errorf("note does not contain %q:\n%s", want, got)
		}
	}
}

func TestACurrentBinaryIsSilent(t *testing.T) {
	p := proxyServing(t, "github.com/go-fleettools/modfloor", "v0.9.0")
	var out strings.Builder
	warnIfStale(&out, p, "github.com/go-fleettools/modfloor", "v0.9.0", 5*time.Second)
	if got := out.String(); got != "" {
		t.Errorf("a current binary warned about itself: %q", got)
	}
}

// ⛔ THE ONE THAT WOULD HAVE BEEN WRONG AS TEXT. v0.10.0 sorts BEFORE v0.9.0
// as a string and after it as semver, and this fleet crosses that boundary
// often — prwait went v0.9.0 -> v0.10.0 the day this was written.
func TestATenthMinorIsAheadOfANinth(t *testing.T) {
	p := proxyServing(t, "github.com/go-fleettools/modfloor", "v0.9.0")
	var out strings.Builder
	warnIfStale(&out, p, "github.com/go-fleettools/modfloor", "v0.10.0", 5*time.Second)
	if got := out.String(); got != "" {
		t.Errorf("v0.10.0 was called older than v0.9.0, which is string order, not semver: %q", got)
	}
}

// A report must not gain noise because the network is down, so every failure
// path is silent. Each of these would print under a naive implementation.
func TestEveryFailureIsSilent(t *testing.T) {
	cases := []struct {
		name            string
		module, version string
		serve           string // the module the proxy knows about
	}{
		{"the proxy has no such module", "github.com/go-fleettools/modfloor", "v0.3.0", "example.com/other"},
		{"the version is not semver", "github.com/go-fleettools/modfloor", "(devel)", "github.com/go-fleettools/modfloor"},
		{"the version is empty", "github.com/go-fleettools/modfloor", "", "github.com/go-fleettools/modfloor"},
		{"the module path is not usable in a URL", "not a module path", "v0.3.0", "github.com/go-fleettools/modfloor"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := proxyServing(t, c.serve, "v0.9.0")
			var out strings.Builder
			warnIfStale(&out, p, c.module, c.version, 5*time.Second)
			if got := out.String(); got != "" {
				t.Errorf("printed %q when it should have stayed quiet", got)
			}
		})
	}
}

// A proxy that answers a version which is not semver must not be compared
// against, and must not panic: the reply is data from the network.
func TestANonSemverReplyIsIgnored(t *testing.T) {
	p := proxyServing(t, "github.com/go-fleettools/modfloor", "not-a-version")
	var out strings.Builder
	warnIfStale(&out, p, "github.com/go-fleettools/modfloor", "v0.3.0", 5*time.Second)
	if got := out.String(); got != "" {
		t.Errorf("compared against a non-semver reply: %q", got)
	}
}

// ⛔ THE TEST FOR THE ONE LINE THE OTHER SIX CANNOT SEE. Every test above
// drives warnIfStale directly, so deleting the call in main would leave them
// all green — a mechanism built and never wired. This one calls main itself.
func TestMainPrintsTheNoteItsLogicProduces(t *testing.T) {
	var out strings.Builder
	swap(t, &staleErr, io.Writer(&out))
	swap(t, &staleProxy, func() *Proxy {
		return proxyServing(t, "github.com/go-fleettools/modfloor", "v99.0.0")
	})
	swap(t, &staleTimeout, 5*time.Second)
	// main must not end the test binary, and must not be judged on its status
	// here: this test is about the note, not about the command's verdict.
	swap(t, &osExit, func(int) {})
	swap(t, &os.Args, []string{"modfloor", "--help"})
	// ⛔ The identity comes from the BINARY's build info, which under `go test`
	// is "(devel)" — so WarnIfStale declines and the note cannot appear however
	// the proxy answers. That is correct behaviour and it makes this assertion
	// unobservable, so the version is supplied the way main's own seam does.
	if _, _, ok := selfBuild(); !ok {
		swap(t, &staleSelf, func() (string, string, bool) {
			return "github.com/go-fleettools/modfloor", "v0.3.0", true
		})
	}

	main()

	if got := out.String(); !strings.Contains(got, "v99.0.0") {
		t.Errorf("main did not print the staleness note; the call in main is missing or unreachable.\ngot: %q", got)
	}
}

// swap sets a variable for the duration of one test and restores it after,
// whatever the test does.
func swap[T any](t *testing.T, p *T, v T) {
	t.Helper()
	old := *p
	*p = v
	t.Cleanup(func() { *p = old })
}

// selfBuild reads this test binary's own build info. Under `go test` the main
// module has version "(devel)", so the contract asserted here is that it
// declines rather than inventing a comparison — which is also what a developer
// running their own tree must see.
func TestABuildFromSourceDeclinesToJudgeItself(t *testing.T) {
	if _, _, ok := selfBuild(); ok {
		t.Skip("this binary carries a released version, so there is nothing to decline")
	}
	var out strings.Builder
	WarnIfStale(&out, proxyServing(t, "github.com/go-fleettools/modfloor", "v99.0.0"), 5*time.Second)
	if got := out.String(); got != "" {
		t.Errorf("a build from source warned about being stale: %q", got)
	}
}
