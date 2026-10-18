# modfloor — the Go toolchain floor a module DECLARES, against the floor a fix REQUIRES

A repository can be green, released, and built end to end with a patched
toolchain while still shipping a vulnerable floor to everyone downstream.

Three pins decide three different things, and they move apart:

| | | |
|---|---|---|
| `go-version:` in a workflow | builds the artefacts | `gopin --from 1.27.1 --version 1.27.2` |
| the toolchain recorded **in a binary** | what one installed executable was built with | `modfloor --bins ~/.local/bin --floor 1.27.2` |
| the **`go` directive** in the published `go.mod` | what a consumer honours under `GOTOOLCHAIN=local` | `modfloor --dir … --floor 1.27.2`, then raise it **and release** |

Only the third is the exposure, and it is the one no green check looks at.

The case that produced this tool, on a sibling repository: CI pinned
`go-version: '1.27.2'`, every installed binary was built with go1.27.2, every
lane was green — and `go.mod` said `go 1.27.1`, under which govulncheck reports
eight advisories reachable through `net/http` and the standard library. The
artefacts were fine. The *contract* was not, and a consumer running
`go install` with `GOTOOLCHAIN=local` on 1.27.1 got the unpatched build.

The negative control is the part worth having. With the directive raised,
`GOTOOLCHAIN=local go build` answers

```
go.mod requires go >= 1.27.2 (running go 1.27.1)
```

— it refuses, instead of silently building the vulnerable binary.

## Usage

```
modfloor --dir ~/Documents/VCS/GIT/localhost --owners 'go-*' --floor 1.27.2
modfloor --bins ~/.local/bin --floor 1.27.2
modfloor --floor 1.27.2 github.com/go-fleettools/fleettools
```

```
REPOSITORY                MODULE                               LOCAL   PUBLISHED  @LATEST  VERDICT
go-fleettools/fleettools  github.com/go-fleettools/fleettools  1.27.2  1.27.2     v0.20.1  ok
go-widgets/toolkit        github.com/go-widgets/toolkit        1.27.1  1.26.4     v0.9.3   BELOW

population: owners go-* — 1149 go.mod seen, 474 kept; set aside: 228 out of
scope, 26 in no work tree, 0 without a module directive, 0 unreadable
474 modules against floor 1.27.2:  ok=23  BELOW=445  no-go=6
```

Verdicts, kept apart because they call for different work:

| | |
|---|---|
| `ok` | the `go` directive is at or above the floor |
| `toolchain` | the directive is below it, but a `toolchain` line pins higher: the build **fails closed** under `GOTOOLCHAIN=local` rather than producing vulnerable code. Not an exposure — but still a build that breaks for a consumer who forbids downloads |
| `BELOW` | the directive is below the floor and nothing pins higher. **This is the exposure** |
| `no-go` | no `go` directive at all |
| `unreadable` | a directive that is not a Go version |

A `NOTE` column says `fixed locally, UNRELEASED` when the working tree is
already at the floor and the published module is not — the state a remediation
campaign needs to see, because the pull request is merged and the work is not
finished.

Exit status: **0** clean, **1** at least one module exposed, **2** the
measurement could not be made. A lane that cannot tell "found a problem" from
"could not look" eventually reads the second as the first.

## What it reads, and what it refuses to do

It reads the **published** `go.mod` from `proxy.golang.org`, which is where a
consumer reads it from too. Not the working tree, which may hold an unreleased
fix; not a workflow file, which describes the build and not the contract.

It spends **no GitHub API budget and needs no credential**. That budget is per
user and shared with the fleet's Renovate runners, which four sweeps have
emptied; and a tool that needs no token cannot leak one.

`--owners` is a **declared** population, and every number in the report is a
number about that set. This is not a convenience: the first run of this tool
walked a development machine without it, picked up the module cache and a
checkout of the Go distribution, and reported *532 modules below the floor*.
Every count was true and the population was wrong — nobody here can raise the
floor of `k8s.io/api`. A module in no git work tree, or under another owner, is
**set aside and counted**, and the counts are printed next to the answer.

The discriminator is not the module path but `remote.origin.url`: the question
"is this ours" is really "can we push a fix". A credential in that URL is
dropped before it can reach the report.

A measurement that found nothing **refuses** rather than reporting a clean
fleet. Zero exposed modules out of zero modules is the best possible value in
its column, which is exactly how an unopened measurement reads as perfection.

## Two traps it encodes

**`go/version` accepts `go27.2`.** It reads 27 as the major, so the common typo
of dropping the `1.` is *valid* and compares **above** every real version — a
`--floor 27.2` would mark the entire fleet as exposed. The floor is validated,
and the error says why.

**`modfile.ParseLax` does not fill `File.Toolchain`.** In lax mode x/mod keeps
only `go`, `module`, `require` and `retract` and drops the rest. That is the one
directive whose loss turns a module which *fails closed* into a reported
exposure, so it is recovered from the syntax tree, and a test asserts the
verdict rather than the mechanism.

## Siblings

For the advisories themselves, `vulnscan` runs govulncheck across the fleet and
`vulngate` judges one run; `gopin` moves the CI pin; `staletools` says which
installed binaries are behind. This tool runs nothing: it reads the contract.

## Install

```
GOBIN=~/.local/bin go install github.com/go-fleettools/modfloor@latest
```

`go install` writes to `GOBIN`, which defaults to `$(go env GOPATH)/bin` —
often not on `PATH`, where an install that landed nowhere looks exactly like one
that worked.
