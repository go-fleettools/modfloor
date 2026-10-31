# modfloor — the Go toolchain floor a module DECLARES, against the floor a fix REQUIRES

A repository can be green, released, and built end to end with a patched
toolchain while still shipping a vulnerable floor to everyone downstream.

Three pins decide three different things, and they move apart:

| | | |
|---|---|---|
| `go-version:` in a workflow | builds the artefacts | `gopin --from 1.27.2 --version 1.27.3` |
| the toolchain recorded **in a binary** | what one installed executable was built with | `modfloor --bins ~/.local/bin --floor 1.27.3` |
| the **`go` directive** in the published `go.mod` | what a consumer honours under `GOTOOLCHAIN=local` | `modfloor --dir … --floor 1.27.3`, then raise it **and release** |

Only the third is the exposure, and it is the one no green check looks at.

⛔ **The floor is 1.27.3 since 2026-10-22**, and the reason is the shape this
tool was built for. `GO-2026-6809` (CVE-2026-111943, *"Distrust compromised
intermediate certificates in `crypto/x509`"*, published 2026-10-21, fixed in
go1.26.10 and go1.27.3) was found **reachable** from this very module — the
trace is `cmd.go:105:12: modfloor.RootCmd calls pflag.FlagSet.BoolVarP, which
eventually calls x509.Certificate.Verify` — while it declared `go 1.27.2`. Same
tree, same `govulncheck`, the directive the only variable: `1.27.2` is affected,
`1.27.3` reports nothing. So "below the floor" and "current until yesterday"
were the same number for a day, which is why this repository's own lane now
plants a module exactly **one patch** below rather than an obviously old one.

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
modfloor --dir ~/Documents/VCS/GIT/localhost --owners 'go-*' --floor 1.27.3
modfloor --bins ~/.local/bin --floor 1.27.3
modfloor --floor 1.27.3 github.com/go-fleettools/fleettools
```

A recorded run, kept as it was measured — against the floor that was current
then (1.27.2), not the one above:

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

And the exposure is **split by whether anybody can reach it**, because one
number over both populations cannot be prioritised: a module on the proxy
imposes its floor on every consumer that runs `go install`, while one that was
never published imposes it only on somebody building from a clone. A third line
counts the modules already fixed in their working tree and **not released**,
which is the state where the pull request is merged and the work is not
finished.

```
939 modules declare a floor below 1.27.3 …
   of those, 904 are PUBLISHED on the proxy (a consumer can install them)
   and 35 were never published (only a local build honours their floor).
   and 3 already carry the fix in their working tree but have not been RELEASED…
```

⛔ Worth saying plainly: that split was added because it **might** have shrunk
the campaign, and on this fleet it did not — 904 of 939 are installable. A
measurement is worth taking when either answer would change what you do, not
only when it gives the answer you hoped for.

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

## `modfloor pseudo`: dependencies that were never released

```
modfloor pseudo --dir ~/Documents/VCS/GIT/github.com/go-llmflow/gateway \
  --fleet-prefix github.com/go-volumes/ --fleet-prefix github.com/go-authn/
```

```
MODULE                                     VERSION                             FLEET
github.com/go-volumes/interface            v0.0.0-20261026102434-e5b421ad6fae  FLEET
google.golang.org/genproto/googleapis/rpc  v0.0.0-20260928230214-8a89bd6388cc
2 of 120 modules at a pseudo-version, 1 of them the fleet's
```

This lists every module of a build graph that is required at a pseudo-version, that is at a commit instead of a tag. It reads `go list -m -json all`, run with `GOWORK=off` in `--dir`, or a saved copy given with `--input`.

A replaced module is judged by what is actually built. Modules matching a `--fleet-prefix` are marked `FLEET`; without any prefix, every module counts as the fleet's.

**Why it matters.** A pseudo-version names a commit that semver says nothing about, and it stays in the module proxy forever. On 2026-10-26, go-llmflow/gateway depended this way on two fleet modules that had never been tagged.

**Exit status.**

- 0: no fleet module is at a pseudo-version. Third-party modules at a pseudo-version are listed but do not fail the check, because their upstream decides whether to tag.
- 1: at least one fleet module is at a pseudo-version.
- 2: the graph could not be read. A graph with no dependency also exits 2: it was not measured, so it is not reported clean.

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
