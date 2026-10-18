// Command modfloor measures the Go toolchain floor a module DECLARES against
// the floor a security fix REQUIRES.
//
// See the command's own --help, or the package's longHelp, for what the three
// pins are and why only one of them is an exposure.
package main

import (
	"errors"
	"fmt"
	"os"
)

// osExit is a variable so that a test can observe the status without ending
// the test binary.
var osExit = os.Exit

func main() {
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
