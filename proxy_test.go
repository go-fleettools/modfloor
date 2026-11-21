package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFilterTaggedVersions(t *testing.T) {
	input := []string{
		"v0.1.0",
		"v0.2.0-rc1",
		"v0.2.0",
		"v2.0.0+incompatible",
		"v1.0.0-beta.2",
		"not-a-semver",
		"1.0.0", // missing leading 'v'
		"v1.5.0",
	}
	got := FilterTaggedVersions(input)
	want := []string{"v0.1.0", "v0.2.0", "v1.5.0"}
	if len(got) != len(want) {
		t.Fatalf("FilterTaggedVersions: got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("FilterTaggedVersions[%d]: got %q, want %q", i, got[i], want[i])
		}
	}
}

func TestMaxTaggedVersion(t *testing.T) {
	tests := []struct {
		name     string
		versions []string
		want     string
	}{
		{
			name:     "empty",
			versions: nil,
			want:     "",
		},
		{
			name:     "only prereleases and incompatible",
			versions: []string{"v1.0.0-rc1", "v2.0.0+incompatible", "invalid"},
			want:     "",
		},
		{
			name:     "picks semver max not lexical",
			versions: []string{"v0.1.0", "v0.10.0", "v0.2.0"},
			want:     "v0.10.0",
		},
		{
			name:     "issue 16 example",
			versions: []string{"v0.3.0", "v0.1.0", "v0.2.0"},
			want:     "v0.3.0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MaxTaggedVersion(tt.versions)
			if got != tt.want {
				t.Errorf("MaxTaggedVersion: got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestProxyResolve(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		switch path {
		case "example.com/settled/@v/list":
			fmt.Fprintln(w, "v0.1.0\nv0.2.0\nv0.3.0")
		case "example.com/settled/@latest":
			fmt.Fprintln(w, `{"Version":"v0.3.0"}`)

		case "example.com/unsettled/@v/list":
			// Issue #16: @v/list already has v0.3.0, @latest still answers v0.2.0
			fmt.Fprintln(w, "v0.1.0\nv0.2.0\nv0.3.0")
		case "example.com/unsettled/@latest":
			fmt.Fprintln(w, `{"Version":"v0.2.0"}`)

		case "example.com/untagged/@v/list":
			// No tagged releases
			fmt.Fprintln(w, "")
		case "example.com/untagged/@latest":
			fmt.Fprintln(w, `{"Version":"v0.0.0-20261115120000-abcdef123456"}`)

		case "example.com/nolist/@v/list":
			w.WriteHeader(http.StatusNotFound)
		case "example.com/nolist/@latest":
			fmt.Fprintln(w, `{"Version":"v1.0.0"}`)

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	p := &Proxy{Base: srv.URL, Client: srv.Client()}
	ctx := context.Background()

	t.Run("settled", func(t *testing.T) {
		res, err := p.Resolve(ctx, "example.com/settled")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.Version != "v0.3.0" || !res.FromList || res.Disagreed {
			t.Errorf("settled: got %+v, want Version=v0.3.0, FromList=true, Disagreed=false", res)
		}
	})

	t.Run("unsettled proxy window (issue 16)", func(t *testing.T) {
		res, err := p.Resolve(ctx, "example.com/unsettled")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.Version != "v0.3.0" {
			t.Errorf("expected version from @v/list (v0.3.0), got %q", res.Version)
		}
		if !res.FromList {
			t.Errorf("expected FromList=true")
		}
		if !res.Disagreed {
			t.Errorf("expected Disagreed=true when @latest lags @v/list")
		}
		if res.Endpoint != "v0.2.0" {
			t.Errorf("expected Endpoint=v0.2.0, got %q", res.Endpoint)
		}
	})

	t.Run("untagged fallback", func(t *testing.T) {
		res, err := p.Resolve(ctx, "example.com/untagged")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.Version != "v0.0.0-20261115120000-abcdef123456" || res.FromList || res.Disagreed {
			t.Errorf("untagged: got %+v, want fallback to @latest", res)
		}
	})

	t.Run("404 list fallback", func(t *testing.T) {
		res, err := p.Resolve(ctx, "example.com/nolist")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.Version != "v1.0.0" || res.FromList {
			t.Errorf("nolist: got %+v, want fallback to @latest", res)
		}
	})
}

func TestReadPublishedSettlingWindowNote(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		switch path {
		case "example.com/lag/@v/list":
			fmt.Fprintln(w, "v0.1.0\nv0.2.0\nv0.3.0")
		case "example.com/lag/@latest":
			fmt.Fprintln(w, `{"Version":"v0.2.0"}`)
		case "example.com/lag/@v/v0.3.0.mod":
			fmt.Fprint(w, "module example.com/lag\n\ngo 1.27.4\n")
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	p := &Proxy{Base: srv.URL, Client: srv.Client()}
	row := Row{Module: "example.com/lag"}
	read := readPublished(context.Background(), p, &row)

	if !read.exists {
		t.Fatalf("expected read.exists=true")
	}
	if row.Latest != "v0.3.0" {
		t.Errorf("expected row.Latest=v0.3.0 from @v/list, got %q", row.Latest)
	}
	if row.Published.Go != "1.27.4" {
		t.Errorf("expected row.Published.Go=1.27.4, got %q", row.Published.Go)
	}
	if !strings.Contains(row.Note, "proxy has not settled") {
		t.Errorf("expected note to mention proxy settling window, got: %q", row.Note)
	}
	if !strings.Contains(row.Note, "v0.2.0") || !strings.Contains(row.Note, "v0.3.0") {
		t.Errorf("expected note to name both versions, got: %q", row.Note)
	}
}
