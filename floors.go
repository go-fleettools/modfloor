// Command modfloor measures the Go toolchain floor a module DECLARES against
// the floor a security fix REQUIRES.
//
// A repository can be green, released, and built end to end with a patched
// toolchain while still shipping a vulnerable floor to everyone downstream.
// The three facts are independent:
//
//   - the toolchain CI pins (`go-version:` in a workflow) builds the artefacts;
//   - the toolchain that built an installed binary is recorded in the binary;
//   - the `go` directive in the PUBLISHED go.mod is what a consumer honours
//     when it runs `go install` with GOTOOLCHAIN=local.
//
// Only the third one is the exposure, and it is the one no green check looks
// at. modfloor reads it from the module proxy, which is where a consumer reads
// it from too — not from the working tree, which may hold an unreleased fix,
// and not from a workflow file, which describes the build and not the contract.
package main

import (
	"fmt"
	"go/version"
	"strings"

	"golang.org/x/mod/modfile"
)

// Floors are the toolchain requirements one go.mod declares.
//
// Go and Toolchain are kept in the spelling the file used: the `go` directive
// is bare ("1.27.1") and the `toolchain` directive carries the prefix
// ("go1.27.2"). Normalising happens at comparison time, so that a report can
// quote the file.
type Floors struct {
	Module    string // the `module` path, "" if the file declares none
	Go        string // the `go` directive, e.g. "1.27.1"; "" if absent
	Toolchain string // the `toolchain` directive, e.g. "go1.27.2"; "" if absent
}

// ParseFloors reads the three directives modfloor needs out of a go.mod.
//
// golang.org/x/mod/modfile is the parser the go command itself uses, so a
// go.mod this tool can read is exactly one the toolchain can read. The first
// version of this file hand-parsed three line prefixes, which approximated the
// grammar: it would have read a `go` line inside a parenthesised block, and it
// trusted that a directive never continues across lines. Approximating a
// grammar is how a measurement ends up disagreeing with the thing it measures.
//
// ParseLax, not Parse: a go.mod carrying a statement this version of x/mod does
// not know is still a go.mod whose floor we can read, and refusing the whole
// file over an unknown keyword would turn a readable module into a gap in the
// census.
//
// ⛔ ParseLax DOES NOT FILL File.Toolchain. In lax mode x/mod keeps only the
// verbs that matter for a dependency — go, module, require, retract — and
// silently drops the rest, `toolchain` among them. Measured, not assumed: a
// go.mod carrying `toolchain go1.27.2` came back with Toolchain == "".
//
// That is the one directive this tool cannot afford to lose, because it is what
// separates a module that FAILS CLOSED under GOTOOLCHAIN=local from one that
// builds vulnerable code — the difference between the "toolchain" verdict and
// "BELOW". Losing it would have turned every pinned module into a false alarm,
// and a tool that flags what is already safe is a tool that gets routed around.
//
// So the directive is read from the syntax tree, which lax mode does populate.
func ParseFloors(src []byte) (Floors, error) {
	f, err := modfile.ParseLax("go.mod", src, nil)
	if err != nil {
		return Floors{}, err
	}
	var fl Floors
	if f.Module != nil {
		fl.Module = f.Module.Mod.Path
	}
	if f.Go != nil {
		fl.Go = f.Go.Version
	}
	if f.Toolchain != nil {
		fl.Toolchain = f.Toolchain.Name
	} else {
		fl.Toolchain = toolchainFromSyntax(f)
	}
	return fl, nil
}

// toolchainFromSyntax recovers the `toolchain` directive that ParseLax drops.
//
// The grammar is still x/mod's: this reads the tokens the real parser produced,
// it does not re-lex the file. A `toolchain` inside a parenthesised block is not
// a toolchain directive, so only top-level lines are considered.
func toolchainFromSyntax(f *modfile.File) string {
	if f.Syntax == nil {
		return ""
	}
	for _, stmt := range f.Syntax.Stmt {
		line, ok := stmt.(*modfile.Line)
		if !ok || len(line.Token) != 2 || line.Token[0] != "toolchain" {
			continue
		}
		return line.Token[1]
	}
	return ""
}

// A Verdict says what a declared floor means for a consumer, and the four
// outcomes are deliberately not collapsed into pass/fail: they call for
// different work.
type Verdict string

const (
	// OK: the `go` directive is at or above the required floor. Nothing to do.
	OK Verdict = "ok"

	// Pinned: the directive is below the floor but a `toolchain` line asks for
	// a patched toolchain. A consumer with GOTOOLCHAIN=auto builds safely; one
	// with GOTOOLCHAIN=local gets an ERROR rather than a vulnerable build. It
	// fails closed, so it is not an exposure — but it is still a build that
	// breaks for a consumer who forbids downloads.
	Pinned Verdict = "toolchain"

	// Below: the directive is below the floor and nothing pins higher. This is
	// the exposure.
	Below Verdict = "BELOW"

	// NoFloor: no `go` directive at all. Older than the floor by construction.
	NoFloor Verdict = "no-go"

	// Unreadable: a directive is present but not a valid Go version. Reported
	// rather than silently treated as absent, because "" and "garbage" call for
	// different fixes.
	Unreadable Verdict = "unreadable"
)

// Judge compares one set of declared floors against a required floor.
//
// floor may be written either way ("1.27.2" or "go1.27.2").
func Judge(f Floors, floor string) Verdict {
	want := goVersion(floor)
	if f.Go == "" {
		return NoFloor
	}
	got := goVersion(f.Go)
	if !version.IsValid(got) {
		return Unreadable
	}
	if version.Compare(got, want) >= 0 {
		return OK
	}
	if tc := goVersion(f.Toolchain); f.Toolchain != "" && version.IsValid(tc) &&
		version.Compare(tc, want) >= 0 {
		return Pinned
	}
	return Below
}

// goVersion normalises a directive to the "go1.27.2" spelling that
// go/version understands. The `go` directive omits the prefix and the
// `toolchain` directive carries it, so both spellings reach here.
func goVersion(s string) string {
	if s == "" {
		return ""
	}
	if strings.HasPrefix(s, "go") {
		return s
	}
	return "go" + s
}

// Exposed reports whether a verdict describes a consumer who can end up
// building vulnerable code. It exists so that the exit status and the printed
// table cannot drift apart: both ask this one question.
func (v Verdict) Exposed() bool {
	return v == Below || v == NoFloor
}

// ValidateFloor rejects a required floor that would make the whole report
// wrong, which is not the same as one go/version cannot parse.
//
// go/version accepts "go27.2" — it reads 27 as the major — so the common typo
// of dropping the "1." is VALID and compares ABOVE every real version. A floor
// of "27.2" would therefore mark every module in the fleet as exposed: a false
// alarm rather than a silent pass, but a tool that flags everything is a tool
// that gets routed around.
//
// Every Go release has major 1, so the floor must too. If Go 2 ever ships,
// this is the line to change, and the test that pins it says the same.
func ValidateFloor(floor string) error {
	if floor == "" {
		return fmt.Errorf("no floor given")
	}
	got := goVersion(floor)
	if !version.IsValid(got) {
		return fmt.Errorf("floor %q is not a Go version (want e.g. 1.27.2)", floor)
	}
	if got != "go1" && !strings.HasPrefix(got, "go1.") {
		return fmt.Errorf("floor %q names major %s; every Go release has major 1, "+
			"so this is almost certainly a dropped \"1.\" — and it would compare "+
			"ABOVE every real version, marking the whole fleet exposed",
			floor, version.Lang(got))
	}
	return nil
}
