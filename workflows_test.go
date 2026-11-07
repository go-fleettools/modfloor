package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ⛔ THIS REPOSITORY'S SECURITY LANE CALLED A COBRA COMMAND WITH ONE DASH.
// `vulngate -go` in ci.yml, against a pin of fleettools v0.72.0 — well past the
// v0.53.0 cobra migration — so it only worked through the compatibility shim,
// which prints a warning on every run and announces its own removal. The lane
// was GREEN and carried that warning, which is a warning nobody reads, and the
// day the shim goes it fails for a reason unrelated to any vulnerability.
//
// Second occurrence of the class: fleettools v0.82.1 fixed eight of these in
// its own workflows the same day, one of them in a file nobody had looked at.
// A lesson that recurs becomes a gate.
//
// ⛔ What this test does NOT know: the full list of fleettools commands. This
// module calls exactly one, so the gate names it rather than pretending to a
// coverage it has not got — an honest narrow check beats a wide one that is
// really a guess. fleettools' own
// TestNoWorkflowCallsOurOwnCommandsWithASingleDash enumerates `cmd/` and is the
// wide version, in the repository that can see the list.
func TestNoWorkflowCallsAFleetCommandWithASingleDash(t *testing.T) {
	const ours = "vulngate"

	files, err := filepath.Glob(filepath.Join(".github", "workflows", "*.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		// Nothing read is not the same as nothing wrong: this repository has a
		// workflow, so finding none means the glob is broken.
		t.Fatal("no workflow read, so nothing was checked")
	}

	var bad, calls []string
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(b), "\n") {
			// A comment is not a call site: refusing what the thing being
			// protected accepts is as bad as missing a real one.
			if strings.HasPrefix(strings.TrimLeft(line, " \t"), "#") {
				continue
			}
			flags, called := singleDashAfter(line, ours)
			if called {
				calls = append(calls, filepath.Base(f))
			}
			for _, fl := range flags {
				bad = append(bad, filepath.Base(f)+" "+fl)
			}
		}
	}

	// ⛔ The planted positive this gate needs: if the call site is ever renamed
	// or removed, a scan over zero calls passes for ever and says nothing.
	if len(calls) == 0 {
		t.Fatalf("no workflow invokes %s at all; this gate is now vacuous and should "+
			"either name the command that replaced it or be deleted", ours)
	}
	if len(bad) > 0 {
		t.Errorf("%d call site(s) spell a cobra flag with one dash: %s", len(bad), strings.Join(bad, ", "))
	}
}

// And the detector itself, proven in both directions rather than trusted.
func TestTheSingleDashDetectorCanFire(t *testing.T) {
	for _, c := range []struct {
		line string
		want int
		why  string
	}{
		{`          "$(go env GOPATH)/bin/vulngate" -go "$(go env GOVERSION)" /tmp/vuln.json`, 1,
			"the exact shape this repository had"},
		{`          /tmp/vulngate -go x -json y`, 2, "every flag, not just the first"},
		{`          "$(go env GOPATH)/bin/vulngate" --go "$(go env GOVERSION)" /tmp/vuln.json`, 0,
			"the corrected spelling"},
		{`          go install github.com/go-fleettools/fleettools/cmd/vulngate@v0.72.0`, 0,
			"⛔ an install line is not a call: `@v0.72.0` follows the name and `-` never appears"},
		{`          "$(go env GOPATH)/bin/govulncheck" -format json ./...`, 0,
			"⛔ x/vuln's own flag spelling, not ours"},
		{`          /tmp/vulngate --json - x`, 0, "a bare dash is stdin, not a flag"},
	} {
		got, _ := singleDashAfter(c.line, "vulngate")
		if len(got) != c.want {
			t.Errorf("%d flag(s), want %d — %s\n  %s", len(got), c.want, c.why, c.line)
		}
	}
	// The install line must not count as a call either, or the vacuity check
	// above would be satisfied by a line that runs nothing.
	if _, called := singleDashAfter(`          go install …/cmd/vulngate@v0.72.0`, "vulngate"); called {
		t.Error("an install line was counted as a call site")
	}
}

// singleDashAfter returns the single-dash flags a line passes to cmd, and
// whether the line invokes it at all. It scans only AFTER the command's own
// token, because flags before it belong to whatever is running it: `go install
// …/cmd/vulngate@vX` and `go build -o …/vulngate` are not calls to vulngate.
func singleDashAfter(line, cmd string) (flags []string, called bool) {
	fields := strings.Fields(line)
	start := -1
	for i, f := range fields {
		base := filepath.Base(strings.Trim(f, `"'`))
		if base == cmd {
			start = i
			break
		}
	}
	if start < 0 {
		return nil, false
	}
	for _, f := range fields[start+1:] {
		if len(f) > 1 && f[0] == '-' && f[1] != '-' {
			flags = append(flags, f)
		}
	}
	return flags, true
}
