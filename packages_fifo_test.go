//go:build unix

package main

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// ⛔ A FIFO NAMED x.go BLOCKS open() UNTIL SOMEBODY WRITES TO IT. This walk
// runs over trees ~25 sessions and every build tool on this machine write
// into, and a sweep that stops dead says nothing at all — the worst failure
// this package can have. The deadline is in the TEST so the mutant fails in
// seconds instead of hanging the suite.
//
// ⛔ In its own file under //go:build unix: syscall.Mkfifo does not exist on
// Windows, and `GOOS=windows go vet ./...` is a lane of this repository's CI.
// A test constrained to one system is not a test of one system's behaviour —
// the guard it covers protects every platform; only the way to BUILD a FIFO is
// local.
func TestAFifoNamedLikeAGoFileIsNotOpened(t *testing.T) {
	dir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, "x.go"), 0o600); err != nil {
		t.Skip("cannot make a FIFO here:", err)
	}
	type answer struct {
		got bool
		err error
	}
	done := make(chan answer, 1)
	go func() {
		got, err := HasGoPackages(dir)
		done <- answer{got, err}
	}()
	select {
	case a := <-done:
		if a.err != nil {
			t.Fatalf("HasGoPackages = %v: a FIFO is skipped, not an error", a.err)
		}
		if a.got {
			t.Error("a FIFO was counted as a Go package")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("HasGoPackages did not return: it opened the FIFO and is waiting for a writer")
	}
}
