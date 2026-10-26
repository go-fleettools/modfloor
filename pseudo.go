package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"golang.org/x/mod/module"
)

// The pseudo subcommand: the modules of a build graph required at a
// PSEUDO-VERSION (v0.0.0-20261026102434-e5b421ad6fae) rather than a tag.
//
// A pseudo-version names a commit, not a release: nothing says it was meant
// to be consumed, semver says nothing about it, and the module proxy keeps it
// forever. For a fleet module it means a dependency was taken from a branch
// that was never released — on 2026-10-26 go-llmflow/gateway reached
// go-volumes/interface and go-volumes/pool that way, two modules with no tag
// at all. The first version of this check was a scratch program reading
// `go list -m -json all`; it lives here so it is not retyped differently.
//
// Third-party modules at a pseudo-version are reported and do not fail the
// check: their upstream decides whether to tag (genproto never does).

const pseudoHelp = `List the modules of a build graph required at a pseudo-version.

It reads ` + "`go list -m -json all`" + ` in --dir (run with GOWORK=off), or a saved
copy of that output with --input. Every module at a pseudo-version is listed;
those matching a --fleet-prefix are marked FLEET and make the command exit 1.
Without --fleet-prefix every module counts as the fleet's.

Exit status: 0 no fleet module at a pseudo-version, 1 at least one,
2 the graph could not be read.`

// listModules runs `go list -m -json all` in dir. A seam for the tests.
var listModules = func(dir string) ([]byte, error) {
	cmd := exec.Command("go", "list", "-m", "-json", "all")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list -m -json all in %s: %v: %s", dir, err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// pseudoModule is one module required at a pseudo-version.
type pseudoModule struct {
	Path, Version string
	Fleet         bool
}

// findPseudo decodes a `go list -m -json` stream and returns the modules
// at a pseudo-version, and how many modules it read (the main module
// excluded). Reading zero modules is an error: a check that could not read
// the graph must not report it clean.
func findPseudo(r io.Reader, fleet []string) ([]pseudoModule, int, error) {
	dec := json.NewDecoder(r)
	var out []pseudoModule
	n := 0
	for {
		var m struct {
			Path, Version string
			Main          bool
			Replace       *struct{ Path, Version string }
		}
		err := dec.Decode(&m)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, n, fmt.Errorf("reading the module list: %w", err)
		}
		if m.Main {
			continue
		}
		n++
		v := m.Version
		if m.Replace != nil && m.Replace.Version != "" {
			v = m.Replace.Version // what is actually built
		}
		if v == "" || !module.IsPseudoVersion(v) {
			continue
		}
		out = append(out, pseudoModule{Path: m.Path, Version: v, Fleet: isFleet(m.Path, fleet)})
	}
	if n == 0 {
		return nil, 0, errors.New("the module list holds no dependency: nothing was measured")
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, n, nil
}

func isFleet(path string, prefixes []string) bool {
	if len(prefixes) == 0 {
		return true
	}
	for _, p := range prefixes {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

func pseudoCmd() *cobra.Command {
	var dir, input string
	var fleet []string
	c := &cobra.Command{
		Use:           "pseudo",
		Short:         "List the modules of a build graph required at a pseudo-version",
		Long:          pseudoHelp,
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var raw []byte
			var err error
			if input != "" {
				raw, err = os.ReadFile(input)
			} else {
				raw, err = listModules(dir)
			}
			if err != nil {
				return &exitError{2, err}
			}
			mods, n, err := findPseudo(bytes.NewReader(raw), fleet)
			if err != nil {
				return &exitError{2, err}
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			if len(mods) > 0 {
				fmt.Fprintln(w, "MODULE\tVERSION\tFLEET")
			}
			fleetN := 0
			for _, m := range mods {
				mark := ""
				if m.Fleet {
					mark = "FLEET"
					fleetN++
				}
				fmt.Fprintf(w, "%s\t%s\t%s\n", m.Path, m.Version, mark)
			}
			_ = w.Flush()
			fmt.Fprintf(cmd.OutOrStdout(), "%d of %d modules at a pseudo-version, %d of them the fleet's\n", len(mods), n, fleetN)
			if fleetN > 0 {
				return &exitError{1, fmt.Errorf("%d fleet module(s) required at a pseudo-version: tag them and require the tags", fleetN)}
			}
			return nil
		},
	}
	f := c.Flags()
	f.StringVarP(&dir, "dir", "d", ".", "the module whose build graph is read")
	f.StringVarP(&input, "input", "i", "", "a saved `go list -m -json all` output, instead of running it")
	f.StringSliceVar(&fleet, "fleet-prefix", nil, "module path prefix of the fleet (repeatable), e.g. github.com/go-volumes/")
	return c
}
