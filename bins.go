package main

import (
	"debug/buildinfo"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// A Binary is what one installed executable says about itself.
//
// The fields come from debug/buildinfo, which reads the build information the
// Go linker embeds. They are TYPED: a GoVersion that does not exist is a
// compile error here, where the equivalent `awk` pattern looking for a line
// that does not exist returns the empty string and reads as "the binary does
// not say".
type Binary struct {
	File      string // path on disk
	Module    string // main module path
	Version   string // main module version, or "(devel)"
	GoVersion string // the toolchain that built it, e.g. "go1.27.2"
}

// Walk is what a scan of a directory found, with completeness reported
// separately from the answer: a directory full of unreadable files must not be
// reported as a directory full of fine binaries.
type Walk struct {
	Binaries []Binary
	NotGo    int      // files that are executables but carry no Go build info
	Errors   []string // files that could not be examined at all
}

// ReadBinaries reports the build information of every Go executable directly in
// dir. It does not recurse: a bin directory is flat, and recursing into a
// module cache would take hours.
func ReadBinaries(dir string) (Walk, error) {
	var w Walk
	entries, err := os.ReadDir(dir)
	if err != nil {
		return w, err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		path := filepath.Join(dir, e.Name())
		info, err := buildinfo.ReadFile(path)
		if err != nil {
			// A bin directory legitimately holds shell wrappers and symlinks
			// to non-Go tools. Those are counted, not reported as failures.
			if isNotGoBinary(err) {
				w.NotGo++
				continue
			}
			w.Errors = append(w.Errors, fmt.Sprintf("%s: %v", e.Name(), err))
			continue
		}
		w.Binaries = append(w.Binaries, Binary{
			File:      path,
			Module:    info.Main.Path,
			Version:   info.Main.Version,
			GoVersion: info.GoVersion,
		})
	}
	sort.Slice(w.Binaries, func(i, j int) bool {
		return w.Binaries[i].File < w.Binaries[j].File
	})
	return w, nil
}

// isNotGoBinary separates "this file is simply not a Go executable" from "this
// file could not be examined".
//
// debug/buildinfo exports no sentinel for either (`errNotGoExe` and
// `errUnrecognizedFormat` are unexported, and ReadFile is the whole API), so the
// message is the only thing to match on. A string match can rot silently when
// the standard library rewords an error, which is why
// TestReadBinariesReportsTheToolchainThatBuiltABinary puts a real text file in
// the directory and asserts it lands in NotGo: if the wording changes, that
// count goes to zero and the test says so.
func isNotGoBinary(err error) bool {
	// A file that is missing or unreadable is a genuine problem, not a
	// not-a-Go-binary: a bin directory full of broken symlinks must not read
	// as a bin directory full of other people's tools.
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission) {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "unrecognized file format") ||
		strings.Contains(msg, "not a Go executable")
}

// maxTextFile bounds what is read from one go.mod or one git config.
//
// The largest go.mod in the fleet is a few tens of kilobytes. A file far beyond
// that is either not a go.mod at all or is meant to exhaust this process, and a
// walk that visits thousands of files must not be able to be handed one that
// does not fit in memory. os.ReadFile would read whatever it is given.
const maxTextFile = 1 << 20

// readCapped reads at most maxTextFile bytes and says so rather than
// truncating silently, because a truncated go.mod parses perfectly well and
// would simply appear to declare no floor — a wrong answer that looks like a
// finding.
func readCapped(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	body, err := io.ReadAll(io.LimitReader(f, maxTextFile+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxTextFile {
		return nil, fmt.Errorf("%s: larger than %d bytes, refusing to read it as text", path, maxTextFile)
	}
	return body, nil
}

// FindGoMods walks a tree and reports every go.mod it holds, skipping the
// directories that hold other people's code or no code at all.
//
// testdata is skipped because a corpus routinely carries deliberately broken
// go.mod files, and counting those as fleet modules would put fabricated
// defects in the report.
func FindGoMods(root string) ([]string, []string, error) {
	var found, problems []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", path, err))
			return nil // an unreadable directory is recorded, not fatal
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", "testdata", "_vendor":
				return fs.SkipDir
			}
			return nil
		}
		if d.Name() == "go.mod" {
			found = append(found, path)
		}
		return nil
	})
	sort.Strings(found)
	return found, problems, err
}
