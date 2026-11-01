package main

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// A Repo is the host coordinates of the work tree a go.mod lives in.
//
// This is the discriminator that matters for a remediation campaign, and it is
// not the module path: a walk of a development machine finds hundreds of
// go.mod files in module caches, vendored trees and checkouts of the Go
// distribution itself, every one of them declaring an old floor that nobody
// here can raise. The question "is this ours" is really "can we push a fix",
// and the answer is written in remote.origin.url.
type Repo struct {
	Root  string // the work tree root
	Host  string // e.g. github.com
	Owner string // e.g. go-fleettools
	Name  string // e.g. fleettools
}

func (r Repo) Slug() string {
	if r.Owner == "" {
		return ""
	}
	return r.Owner + "/" + r.Name
}

// Declares reports whether this repository is the one that PUBLISHES the given
// module path — the only case in which raising its floor is something we can do.
//
// ⛔⛔ BEING IN OUR WORK TREE DOES NOT MAKE THE MODULE OURS, and the comment
// above this type said it did. On 2026-11-01 a baseline of this fleet came back
// holding `sigs.k8s.io/yaml`, `cel.dev/expr` and twenty more, all found inside
// `tannevaled/pantry` — a repository that carries other projects' sources. Every
// `remote.origin.url` said tannevaled, so every one passed the owner test, and
// nobody here can raise the floor of `sigs.k8s.io/yaml`. It is the 532-modules
// incident this file was written for, arriving through the other door: the first
// one came from go.mod files in NO repository, this one from foreign go.mod
// files in OURS.
//
// The test is the module path against the repository's own path. A module in a
// subdirectory (a monorepo) and a /v2 suffix both match by prefix; a vanity
// host such as `sigs.k8s.io` can never match a forge slug, which is the point.
// With no remote there is nothing to compare, and the owner globs have already
// had their say, so it is left alone rather than guessed at.
func (r Repo) Declares(module string) bool {
	if r.Host == "" || r.Owner == "" || r.Name == "" {
		return true
	}
	prefix := r.Host + "/" + r.Owner + "/" + r.Name
	return module == prefix || strings.HasPrefix(module, prefix+"/")
}

// ErrNoRepo means no .git was found above the given directory: a module cache
// entry, an unpacked archive, a scratch directory.
var ErrNoRepo = errors.New("not in a git work tree")

// repoCache keys by work tree root, because a repository with forty go.mod
// files must not have its config read forty times.
type repoFinder struct {
	byRoot map[string]Repo
}

func newRepoFinder() *repoFinder { return &repoFinder{byRoot: map[string]Repo{}} }

// Of reports the repository the given directory belongs to.
func (f *repoFinder) Of(dir string) (Repo, error) {
	root, gitdir, err := findGit(dir)
	if err != nil {
		return Repo{}, err
	}
	if r, ok := f.byRoot[root]; ok {
		return r, nil
	}
	r := Repo{Root: root}
	url, err := originURL(gitdir)
	if err == nil {
		r.Host, r.Owner, r.Name = splitRemote(url)
	}
	f.byRoot[root] = r
	return r, nil
}

// findGit walks up looking for .git, and returns the work tree root together
// with the directory that holds config.
//
// Both shapes are handled: a .git directory, and the .git FILE a linked
// worktree has, whose config lives in the common directory two levels above
// the gitdir it names. Worktrees are the normal case here — parallel sessions
// each work in their own.
func findGit(dir string) (root, gitdir string, err error) {
	dir, err = filepath.Abs(dir)
	if err != nil {
		return "", "", err
	}
	for {
		candidate := filepath.Join(dir, ".git")
		info, err := os.Lstat(candidate)
		switch {
		case err == nil && info.IsDir():
			return dir, candidate, nil
		case err == nil:
			// A .git file: "gitdir: /path/to/common/.git/worktrees/name".
			body, err := readCapped(candidate)
			if err != nil {
				return "", "", err
			}
			target := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(body)), "gitdir:"))
			if i := strings.Index(target, string(filepath.Separator)+"worktrees"+string(filepath.Separator)); i >= 0 {
				target = target[:i]
			}
			return dir, target, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", "", ErrNoRepo
		}
		dir = parent
	}
}

// originURL reads remote.origin.url out of a git config.
//
// The INI is parsed here rather than asked of `git config`, because this tool
// must not depend on an external program being on PATH to answer a question
// about a file on disk, and because a credential must never be able to reach a
// command line — reading is strictly safer than running.
func originURL(gitdir string) (string, error) {
	body, err := readCapped(filepath.Join(gitdir, "config"))
	if err != nil {
		return "", err
	}
	inOrigin := false
	for _, raw := range strings.Split(string(body), "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "[") {
			inOrigin = strings.HasPrefix(line, `[remote "origin"]`)
			continue
		}
		if !inOrigin {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) != "url" {
			continue
		}
		return strings.TrimSpace(val), nil
	}
	return "", fmt.Errorf("no remote.origin.url in %s", gitdir)
}

// splitRemote reduces the three spellings of a git remote to host, owner and
// repository name. A URL carrying a credential is reduced the same way and the
// credential is DROPPED here, so that it cannot reach a report.
func splitRemote(url string) (host, owner, name string) {
	s := url
	// scp-like: git@github.com:owner/repo.git
	if !strings.Contains(s, "://") {
		if at := strings.Index(s, "@"); at >= 0 {
			s = s[at+1:]
		}
		h, rest, ok := strings.Cut(s, ":")
		if !ok {
			return "", "", ""
		}
		host = h
		s = rest
	} else {
		_, rest, _ := strings.Cut(s, "://")
		// Anything before an @ is userinfo, which may be a token. Dropped.
		if at := strings.Index(rest, "@"); at >= 0 {
			if slash := strings.Index(rest, "/"); slash < 0 || at < slash {
				rest = rest[at+1:]
			}
		}
		h, p, ok := strings.Cut(rest, "/")
		if !ok {
			return "", "", ""
		}
		host, s = h, p
	}
	s = strings.TrimSuffix(strings.Trim(s, "/"), ".git")
	owner, name, _ = strings.Cut(s, "/")
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:] // a GitLab subgroup path keeps only the project
	}
	return host, owner, name
}

// Owners is a declared population: the owners whose repositories this run is
// about.
//
// It is a flag and not a default, because every number in the report is a
// number ABOUT this set. A walk of a development machine that silently
// included the module cache once reported 532 modules below the floor, of
// which the overwhelming majority were other people's and unfixable here — a
// true count of the wrong population.
type Owners []string

func ParseOwners(s string) Owners {
	var o Owners
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			o = append(o, p)
		}
	}
	return o
}

// Match reports whether an owner is in the population. An empty Owners matches
// everything, which is the right behaviour for -owners being absent: the report
// then shows the OWNER column and claims nothing about a fleet.
func (o Owners) Match(owner string) bool {
	if len(o) == 0 {
		return true
	}
	for _, pat := range o {
		if ok, err := path.Match(pat, owner); err == nil && ok {
			return true
		}
	}
	return false
}

func (o Owners) String() string {
	if len(o) == 0 {
		return "(every owner)"
	}
	return strings.Join(o, ",")
}
