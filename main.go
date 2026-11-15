// Command modfloor measures the Go toolchain floor a module DECLARES against
// the floor a security fix REQUIRES.
//
// See the command's own --help, or the package's longHelp, for what the three
// pins are and why only one of them is an exposure.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// osExit is a variable so that a test can observe the status without ending
// the test binary.
var osExit = os.Exit

// The staleness note's three inputs are variables for one reason: so that a
// test can assert main ACTUALLY CALLS IT. Six tests cover the logic in
// selfstale.go, and none of them would notice the one line in main being
// deleted — which is precisely the defect class `a-mechanism-built-but-never
// -wired`. Verified by mutation: removing that call turns
// TestMainPrintsTheNoteItsLogicProduces red.
var (
	staleErr     io.Writer = os.Stderr
	staleProxy             = NewProxy
	staleTimeout           = 2 * time.Second
)

func main() {
	// ⛔ WIRED IN main AND NOWHERE ELSE, deliberately. A mechanism that exists
	// and is never called is the defect recorded as `a-mechanism-built-but-never
	// -wired`, so this line is the whole point of selfstale.go. It sits here
	// rather than in RootCmd so that the tests, which drive the command
	// directly, never reach the network for it.
	//
	// 2s: long enough for one proxy call on a working connection, short enough
	// that a report is not held up by a broken one. Failure is silent by
	// contract — see WarnIfStale.
	WarnIfStale(staleErr, staleProxy(), staleTimeout)

	err := RootCmd().Execute()
	if err == nil {
		return
	}
	var ee *exitError
	if errors.As(err, &ee) {
		// An exposure has already been described by the table; only a failure
		// to measure needs saying again, and it is said on stderr with the
		// mark the rest of the fleet's tools use.
		if ee.code != 1 {
			fmt.Fprintln(os.Stderr, "⛔", ee.err)
		}
		osExit(ee.code)
		return
	}
	fmt.Fprintln(os.Stderr, "⛔", err)
	osExit(2)
}
