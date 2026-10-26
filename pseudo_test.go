package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A `go list -m -json all` stream with a PLANTED fleet pseudo-version (the
// shape go-llmflow/gateway had on 2026-10-26), a third-party one, a tagged
// fleet module, and a replaced module whose replacement is a pseudo-version.
const graph = `{"Path":"example.com/main","Main":true}
{"Path":"github.com/go-volumes/interface","Version":"v0.0.0-20261026102434-e5b421ad6fae"}
{"Path":"github.com/go-volumes/s3","Version":"v0.2.2"}
{"Path":"google.golang.org/genproto/googleapis/rpc","Version":"v0.0.0-20260928230214-8a89bd6388cc"}
{"Path":"github.com/go-volumes/pool","Version":"v0.1.0","Replace":{"Path":"github.com/go-volumes/pool","Version":"v0.1.1-0.20261026102537-764e93f13b66"}}
{"Path":"github.com/spf13/cobra","Version":"v1.10.2"}
`

func TestFindPseudoSeesThePlantedOnes(t *testing.T) {
	mods, n, err := findPseudo(strings.NewReader(graph), []string{"github.com/go-volumes/"})
	if err != nil || n != 5 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	got := map[string]bool{}
	for _, m := range mods {
		got[m.Path] = m.Fleet
	}
	if len(mods) != 3 || !got["github.com/go-volumes/interface"] || !got["github.com/go-volumes/pool"] ||
		got["google.golang.org/genproto/googleapis/rpc"] {
		t.Fatalf("%+v", mods)
	}
	if _, ok := got["github.com/go-volumes/s3"]; ok {
		t.Fatal("a tagged module was reported")
	}
}

func TestFindPseudoRefusesAnEmptyGraph(t *testing.T) {
	if _, _, err := findPseudo(strings.NewReader(`{"Path":"m","Main":true}`), nil); err == nil {
		t.Fatal("a graph with no dependency was reported clean")
	}
	if _, _, err := findPseudo(strings.NewReader(`{not json`), nil); err == nil {
		t.Fatal("garbage was reported clean")
	}
}

func TestPseudoCommandExitStatus(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "graph.json")
	if err := os.WriteFile(in, []byte(graph), 0o600); err != nil {
		t.Fatal(err)
	}
	// A fleet module at a pseudo-version: 1, and named.
	code, out, _ := runCmd(t, "pseudo", "--input", in, "--fleet-prefix", "github.com/go-volumes/")
	if code != 1 || !strings.Contains(out, "github.com/go-volumes/interface") || !strings.Contains(out, "FLEET") {
		t.Errorf("planted fleet pseudo-version: exit %d\n%s", code, out)
	}
	// Only third-party ones: 0, still listed.
	code, out, _ = runCmd(t, "pseudo", "--input", in, "--fleet-prefix", "github.com/go-nothing/")
	if code != 0 || !strings.Contains(out, "genproto") {
		t.Errorf("third-party only: exit %d\n%s", code, out)
	}
	// Unreadable: 2.
	if code, _, _ := runCmd(t, "pseudo", "--input", filepath.Join(dir, "absent")); code != 2 {
		t.Errorf("unreadable input: exit %d, want 2", code)
	}
}

// The real `go list` path, on a module with no dependency: 2, not clean.
func TestPseudoRunsGoList(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/lonely\n\ngo 1.27.3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := runCmd(t, "pseudo", "--dir", dir); code != 2 {
		t.Errorf("a module with no dependency: exit %d, want 2", code)
	}
}
