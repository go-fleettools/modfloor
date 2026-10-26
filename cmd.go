package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

// options are what the flags decide. They are gathered in one value so that a
// test can drive the measurement directly as well as through the command.
type options struct {
	dir      string
	bins     string
	floor    string
	owners   Owners
	jobs     int
	offline  bool
	listOnly bool
	verbose  bool
	modules  []string // module paths named as positional arguments
}

// An exitError carries the status a run should end with, keeping the three
// outcomes apart: 0 clean, 1 found an exposure, 2 could not measure. A lane
// that cannot tell "found a problem" from "could not look" eventually reads
// the second as the first.
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

const longHelp = `modfloor measures the Go toolchain floor a module DECLARES against the floor a
security fix REQUIRES.

A repository can be green, released, and built end to end with a patched
toolchain while still shipping a vulnerable floor to everyone downstream. The
three facts are independent:

  the go-version PIN in CI ........ builds the artefacts
  the toolchain in a BINARY ....... what one installed executable was built with
  the go DIRECTIVE in go.mod ...... what a consumer honours when it runs
                                    ` + "`go install`" + ` with GOTOOLCHAIN=local

Only the third is the exposure, and it is the one no green check looks at.
modfloor reads it from the module proxy, which is where a consumer reads it
from too — not from the working tree, which may hold an unreleased fix, and not
from a workflow file, which describes the build and not the contract.

Verdicts:

  ok          the go directive is at or above the floor
  toolchain   directive below, but a toolchain line pins higher: fails CLOSED
              under GOTOOLCHAIN=local rather than building vulnerable code
  BELOW       directive below the floor, nothing pins higher — the exposure
  no-go       no go directive at all
  unreadable  a directive that is not a Go version

The remedy, and the sibling tools — there are THREE pins and they move apart:

  the go directive ...... raise it in a PR, then RELEASE: a consumer reads the
                          published go.mod, not your branch
  the CI pin ............ gopin --from 1.27.2 --version 1.27.3
  an installed binary ... reinstall from the tag; staletools says which are old

For the advisories themselves, vulnscan runs govulncheck across the fleet and
vulngate judges one run. This tool runs nothing and needs no credential: it
reads the contract.

Exit status: 0 clean, 1 at least one module exposed, 2 the measurement could
not be made.`

// RootCmd builds the command. Tests drive it with SetArgs, SetOut and SetErr.
func RootCmd() *cobra.Command {
	var (
		opt    options
		owners string
	)
	c := &cobra.Command{
		Use:   "modfloor [module...]",
		Short: "The Go toolchain floor a module declares, against the floor a fix requires",
		Long:  longHelp,
		Args:  cobra.ArbitraryArgs,
		// Usage after a measurement failure buries the reason under forty
		// lines of help, so it is silenced and the error speaks for itself.
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			opt.owners = ParseOwners(owners)
			opt.modules = args
			return run(cmd, opt)
		},
	}
	f := c.Flags()
	f.StringVarP(&opt.dir, "dir", "d", "", "directory tree to walk for go.mod files (the local clones)")
	f.StringVarP(&opt.bins, "bins", "b", "", "flat directory of installed executables to read build info from")
	f.StringVarP(&opt.floor, "floor", "f", "", "required toolchain floor, e.g. 1.27.2; without it no verdict is given")
	f.StringVarP(&owners, "owners", "o", "", "comma-separated owner globs the report is about, e.g. \"go-*\"; "+
		"a module in no git work tree, or under another owner, is SET ASIDE and counted. "+
		"Empty means every owner, which measures a machine and not a fleet")
	f.IntVarP(&opt.jobs, "jobs", "j", 8, "concurrent proxy queries")
	f.BoolVar(&opt.offline, "offline", false, "do not query the proxy; report what the working trees declare")
	f.BoolVarP(&opt.listOnly, "list", "l", false, "print the module paths found and stop")
	f.BoolVarP(&opt.verbose, "verbose", "v", false, "name every go.mod set aside, and every disagreement between worktrees")
	c.AddCommand(pseudoCmd())
	return c
}

func run(cmd *cobra.Command, opt options) error {
	out, errOut := cmd.OutOrStdout(), cmd.ErrOrStderr()

	if opt.dir == "" && opt.bins == "" && len(opt.modules) == 0 {
		return &exitError{2, fmt.Errorf("nothing to measure: pass --dir, --bins, or a module path")}
	}
	if opt.floor != "" {
		if err := ValidateFloor(opt.floor); err != nil {
			return &exitError{2, err}
		}
	}

	status := 0
	if opt.bins != "" {
		if err := reportBinaries(out, errOut, opt.bins, opt.floor); err != nil {
			return &exitError{2, err}
		}
	}
	if opt.dir != "" || len(opt.modules) > 0 {
		code, _, err := reportModules(out, errOut, opt)
		if err != nil {
			return &exitError{2, err}
		}
		if code > status {
			status = code
		}
	}
	if status != 0 {
		// The table has already said which modules; the error only carries the
		// status, so a lane does not get the same list twice.
		return &exitError{status, errExposed}
	}
	return nil
}

var errExposed = fmt.Errorf("at least one module declares a floor below the required one")
