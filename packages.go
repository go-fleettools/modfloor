package main

import (
	"fmt"
	"go/build/constraint"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Whether a module has Go packages a consumer can build.
//
// ⛔ A `go.mod` IS NOT A PACKAGE. A Hugo landing site is a module whose go.mod
// exists only to pin a theme module; nobody runs `go install
// github.com/go-tex/go-tex.github.io`. Measured on 2026-11-15 over this
// fleet: 351 of 843 rows were `*.github.io`, and ALL SEVEN rows reading
// "fixed locally, UNRELEASED" — the most actionable line the report prints —
// were landing sites. Releasing those seven would have protected nobody and
// silenced an auditor, which is the opposite of the point (#19).
//
// ⛔ AND THE TEST IS THE PACKAGES, NEVER THE NAME. `go-pkgx/go-pkgx.github.io`
// ships `tools/playground`, `tools/versioncheck`, `tools/vulnmods` and
// `tools/wasmstage` as modules in this same sweep: a rule keyed on `*.github.io`
// would exempt real Go code. Aligned with go-fleettools/fleettools#206, which
// asks `gofloor` the same question.

// maxConstraintPrologue bounds what is read from each .go file. A build
// constraint must appear before the package clause, so the answer is always in
// the first few lines; a walk that visits thousands of files must not be able
// to be handed one that does not fit in memory.
const maxConstraintPrologue = 64 << 10

// maxGoFilesScanned bounds the walk of one module. Past it the module is
// reported as HAVING packages: the error direction is deliberate everywhere in
// this file — a wrong answer must add noise to the report, never remove a
// module from the population.
//
// ⛔ A var, not a const, so a test can drive the walk TO this edge instead of
// spelling 20000 in an assertion. A test that writes the implementation's
// constant tests the constant: it reddens on every legitimate change to the
// bound and never catches what it was written for.
var maxGoFilesScanned = 20000

// HasGoPackages reports whether the module tree rooted at dir holds at least
// one .go file that an ordinary build could compile in SOME configuration.
//
// "Some configuration" is the whole point: a module whose only source is
// `foo_windows.go` is installable, and so is one whose only source carries
// `//go:build linux`. A file is discounted only when its constraint cannot be
// satisfied without inventing a tag — `//go:build tools`, `//go:build ignore` —
// which is the idiom a repository uses precisely to say "this is not part of
// the build".
//
// A nested module's directory is not part of this module, so the walk stops at
// any subdirectory holding its own go.mod.
func HasGoPackages(dir string) (bool, error) {
	seen := 0
	found := false
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// ⛔ An unreadable directory must not read as an absence of code:
			// that is the one mistake that would exempt a module silently.
			return fmt.Errorf("%s: %w", path, err)
		}
		if d.IsDir() {
			if path == dir {
				return nil
			}
			if ignoredDir(d.Name()) {
				return fs.SkipDir
			}
			if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil {
				return fs.SkipDir // another module's tree, with its own floor
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") || ignoredFile(d.Name()) {
			return nil
		}
		if !regularFile(path) {
			return nil
		}
		seen++
		if seen > maxGoFilesScanned {
			found = true
			return filepath.SkipAll
		}
		ok, err := fileIsBuildable(path)
		if err != nil {
			return err
		}
		if ok {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return found, nil
}

// ignoredDir lists the directories the go tool itself never builds, plus the
// two that hold other projects' code. testdata is skipped for the same reason
// FindGoMods skips it: a corpus carries deliberately broken sources.
func ignoredDir(name string) bool {
	switch name {
	case "testdata", "node_modules", "vendor", "_vendor":
		return true
	}
	return strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")
}

// ignoredFile: the go tool ignores a file whose name begins with "." or "_".
func ignoredFile(name string) bool {
	return strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")
}

// regularFile reports whether this entry is a file that can be read without
// blocking.
//
// ⛔ A FIFO NAMED foo.go BLOCKS open() UNTIL SOMEBODY WRITES TO IT, and this
// walk runs over trees that ~25 sessions and every build tool on this machine
// write into. A device node is the same hazard with a different shape. Only a
// regular file, or a symlink that resolves to one, is opened — and skipping
// something keeps the module in the population, which is the safe direction.
// It asks THROUGH any link, which is the whole point: WalkDir uses Lstat, so a
// symlink to real code arrives as a symlink, a FIFO stays a FIFO, and a
// dangling link is an error. One os.Stat answers all four cases, so there is no
// fast path on d.Type() and no separate test for ModeSymlink — both would be
// branches no input can tell apart from this line.
func regularFile(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular()
}

// fileIsBuildable reads one file's constraint prologue and asks whether any
// ordinary configuration compiles it.
//
// ⛔ A _test.go file COUNTS. It is Go code compiled under this module's own go
// directive, and counting it keeps the module in the population — the safe
// direction. The unsafe direction would be a test-only module quietly set
// aside.
func fileIsBuildable(path string) (bool, error) {
	src, err := readPrologue(path)
	if err != nil {
		return false, err
	}
	return satisfiable(constraintOf(src)), nil
}

func readPrologue(path string) ([]byte, error) {
	f, err := os.Open(path)
	// ⛔ THE PATH IS IN THE ERROR. Without it the caller reports "permission
	// denied" about a module of hundreds of files, and os.File's methods being
	// nil-safe, deleting this branch would still produce an error — a worse
	// one, about nothing in particular. That is what the test asserts.
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, maxConstraintPrologue))
}

// constraintOf returns the file's build constraint, or nil when it has none
// this walk can use.
//
// A //go:build line wins outright; otherwise every // +build line is ANDed,
// which is what the old syntax meant. Both are looked for only above the
// package clause, where the language puts them.
//
// ⛔ NIL FOR BOTH "no constraint" AND "a constraint the toolchain would
// reject", because they are the same decision downstream: treat the file as
// buildable. One return value rather than a (expr, ok) pair, so there is no
// second branch stating that decision a second time.
func constraintOf(src []byte) constraint.Expr {
	var plus []constraint.Expr
	for _, raw := range strings.Split(string(src), "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "package ") || line == "package" {
			break
		}
		switch {
		case constraint.IsGoBuild(line):
			// ⛔ A constraint the toolchain would reject is not an absence of a
			// constraint and not an absence of code. Treat the file as
			// buildable: the module stays judged and the defect stays visible
			// where it is, in that file. constraint.Parse returns a nil
			// expression with its error, and nil is exactly this function's
			// word for that, so the error needs no branch of its own.
			e, _ := constraint.Parse(line)
			return e
		case constraint.IsPlusBuild(line):
			if e, err := constraint.Parse(line); err == nil {
				plus = append(plus, e)
			}
		}
	}
	// Folded from nil rather than from plus[0], so there is no length branch
	// to test and no index to get wrong: no // +build line leaves e nil, which
	// is this function's word for "no usable constraint".
	var e constraint.Expr
	for _, next := range plus {
		if e == nil {
			e = next
			continue
		}
		e = &constraint.AndExpr{X: e, Y: next}
	}
	return e
}

// maxFreeTags bounds the satisfiability search. Past it the constraint is
// called satisfiable, which keeps the module judged.
//
// ⛔ IT IS AN EXPONENT, so the number matters: 8 free tags is 256 evaluations
// of a tiny expression, and 16 would be 65536 — times maxGoFilesScanned, a
// repository could then cost 1.3e9 evaluations of this walk. Real constraints
// name one to four tags.
//
// A var for the same reason as maxGoFilesScanned: a test drives the search to
// this edge rather than restating the number.
var maxFreeTags = 8

// satisfiable reports whether any assignment of the STANDARD tags makes the
// expression true, with every custom tag false.
//
// ⛔ Eval under ONE assignment is not satisfiability, and getting that wrong
// is how this check would exempt real code: `//go:build !windows` evaluates
// FALSE when "windows" is read as true, yet it builds on every other system.
// The tags a consumer does not have to invent are therefore free variables,
// and the tags they would have to pass with -tags are false.
func satisfiable(e constraint.Expr) bool {
	// ⛔ NIL MEANS "NO USABLE CONSTRAINT", AND THAT MEANS BUILDABLE. Both of the
	// callers' "treat it as buildable" paths — no constraint at all, and a
	// constraint the toolchain itself would reject — are the same decision as
	// this one, so answering true here keeps the three in step rather than
	// leaving a nil to panic on.
	//
	// ⚠ The sweep reports this one "caught by PANIC", which it warns is luck
	// rather than a test. Here there is no other way for it to fail: calling a
	// method on a nil interface panics, full stop. The rows that reach it —
	// a file with no constraint, and one whose constraint does not parse — do
	// assert the value, in TestWhatCountsAsAPackage.
	if e == nil {
		return true
	}
	tags := map[string]bool{}
	collectTags(e, tags)
	var free []string
	for t := range tags {
		if standardTag(t) {
			free = append(free, t)
		}
	}
	if len(free) > maxFreeTags {
		return true
	}
	sort.Strings(free)
	for mask := 0; mask < 1<<len(free); mask++ {
		on := map[string]bool{}
		for i, t := range free {
			if mask&(1<<i) != 0 {
				on[t] = true
			}
		}
		if e.Eval(func(tag string) bool { return on[tag] }) {
			return true
		}
	}
	return false
}

func collectTags(e constraint.Expr, into map[string]bool) {
	switch x := e.(type) {
	case *constraint.TagExpr:
		into[x.Tag] = true
	case *constraint.NotExpr:
		collectTags(x.X, into)
	case *constraint.AndExpr:
		collectTags(x.X, into)
		collectTags(x.Y, into)
	case *constraint.OrExpr:
		collectTags(x.X, into)
		collectTags(x.Y, into)
	}
}

// standardTag reports whether a build tag is one an ordinary build can satisfy
// without -tags: a GOOS, a GOARCH, a toolchain tag, or a release tag.
//
// ⛔ A GOOS MISSING FROM THIS SET IS THE DANGEROUS ERROR, not an extra entry:
// it would make a module whose only source is constrained to that system look
// unbuildable, and that module would leave the population silently.
// TestEveryPlatformTheToolchainSupportsIsAStandardTag checks this set against
// `go tool dist list`, the authority.
func standardTag(tag string) bool {
	if knownOS[tag] || knownArch[tag] {
		return true
	}
	switch tag {
	case "cgo", "unix", "gc", "gccgo", "race", "msan", "asan", "boringcrypto":
		return true
	}
	// Release tags: go1.1 … go1.27 and whatever comes after.
	return strings.HasPrefix(tag, "go1.") || strings.HasPrefix(tag, "go2.")
}

// knownOS and knownArch are the go command's own lists (internal/syslist),
// which are NOT exported, so they are restated here and checked by a test
// against `go tool dist list`. They hold names the toolchain knows, including
// ones it no longer builds for: calling such a name standard costs a row of
// noise, calling it custom costs a module.
var knownOS = map[string]bool{
	"aix": true, "android": true, "darwin": true, "dragonfly": true,
	"freebsd": true, "hurd": true, "illumos": true, "ios": true, "js": true,
	"linux": true, "nacl": true, "netbsd": true, "openbsd": true,
	"plan9": true, "solaris": true, "wasip1": true, "windows": true,
	"zos": true,
}

var knownArch = map[string]bool{
	"386": true, "amd64": true, "amd64p32": true, "arm": true, "armbe": true,
	"arm64": true, "arm64be": true, "loong64": true, "mips": true,
	"mipsle": true, "mips64": true, "mips64le": true, "mips64p32": true,
	"mips64p32le": true, "ppc": true, "ppc64": true, "ppc64le": true,
	"riscv": true, "riscv64": true, "s390": true, "s390x": true,
	"sparc": true, "sparc64": true, "wasm": true,
}
