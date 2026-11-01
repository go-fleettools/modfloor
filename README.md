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

The discriminator is `remote.origin.url` **and** the module path, because the
question "is this ours" is really "can we push a fix" and it takes both halves
to answer. A credential in that URL is dropped before it can reach the report.

⛔ **Being in our work tree does not make the module ours** — the second half,
learnt on 2026-11-01 from this tool's own first baseline. It came back holding
`sigs.k8s.io/yaml`, `cel.dev/expr` and twenty more, every one of them found
inside `tannevaled/pantry`, a repository that carries other projects' sources.
Each `remote.origin.url` said tannevaled, so each one passed the owner test, and
nobody here can raise the floor of `sigs.k8s.io/yaml`. It is the 532-modules
incident above arriving through the other door: that one was go.mod files in *no*
repository, this one foreign go.mod files in *ours*. A module whose path is not
the repository's own path — `github.com/<owner>/<name>`, or a subdirectory or
`/v2` of it — is set aside as `published by another project`, counted on the
population line, and named by `--verbose`.

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

Measured 2026-11-01, with the population printed beside it because a count is
only as good as the set it counts over:

```
population: owners go-*,tannevaled — 1779 go.mod seen, 348 kept; set aside:
410 out of scope, 43 in no work tree, 540 published by another project,
0 without a module directive, 0 unreadable
348 modules against floor 1.27.3:  ok=86  BELOW=260  no-go=2
⛔ 262 modules declare a floor below 1.27.3 …
   they live in 221 repositories, so the campaign is about 221 pull requests
   and not 262.
   of those, 262 are PUBLISHED on the proxy (a consumer can install them)
   and 0 were never published.
   and 6 already carry the fix in their working tree but have not been RELEASED…
```

⛔ **A campaign is costed in pull requests, not in findings.** 262 exposed modules
reads as 262 pieces of work and it is not: several modules of one repository are
raised by one pull request, because the directives they declare land together. On
a sibling campaign 896 stale pins turned out to be 123 pull requests — a factor
of seven — and an estimate seven times too large is an estimate that gets the
campaign refused.

Here the factor is **1.19**: 262 modules in 221 repositories. Said plainly,
because it is the second measurement in this README that did not give the answer
it was taken for. The grouping that saved a sibling campaign seven eighths of its
work saves this one nothing much, and 221 pull requests is the real number to
decide about.

⛔ Worth saying plainly: that split was added because it **might** have shrunk
the campaign, and it did not — all 262 are installable. A measurement is worth
taking when either answer would change what you do, not only when it gives the
answer you hoped for.

⛔⛔ **And the first figure published here was wrong**, which is the more useful
half of the story. On 2026-10-31 this README, the organisation's landing and the
documentation all said *904 of 939*. That count was true over a population that
included **540 modules other projects publish**, sitting inside our own work
trees — the owner test passed them and nothing asked whether we could push a fix
to `sigs.k8s.io/yaml`. The real campaign is 262 modules, and the figure that
found the error was produced by this tool's own first baseline, where a human
reading 567 lines noticed `cel.dev/expr` among them. The number was published on
two pages before anyone looked at the list it came from.

## `--baseline`: a gate for a campaign that cannot be finished in one day

⛔ **A report that is red for ever is not a gate.** 262 of this fleet's published
modules declare a floor below 1.27.3, so `modfloor --floor 1.27.3` exits 1 every
time it runs and will keep doing so for as long as raising them takes. A lane in
that state gets read as broken, then ignored — and the day a *new* module lands
below the floor, nothing changes colour.

So the two questions are separated. "How many are exposed" is a number that
drains slowly. "Did anything get **worse** since the last look" must stay zero,
and that one is a gate.

```
modfloor --dir … --owners 'go-*' --floor 1.27.3 --write-baseline floor.txt
modfloor --dir … --owners 'go-*' --floor 1.27.3 --baseline floor.txt
```

The file is plain text, so the diff of a tightened baseline is readable in a pull
request:

```
# modfloor baseline — the exposed set at a KNOWN floor …
floor 1.27.3
mode published
taken 2026-11-01
BELOW	github.com/go-widgets/toolkit
no-go	example.com/ancient
```

Three refusals, each one a gate that would otherwise pass for the wrong reason:

| | |
|---|---|
| no `floor` line, or **another** floor | every verdict here changes the day the floor moves, and it moved three times in four days (1.27.1 → 1.27.2 → 1.27.3). A baseline is a set of module names, and a set of names says nothing without the floor it was judged against |
| another `mode` | `--offline` records what the working trees declare, the default records what the proxy serves. Those are different answers about the same modules |
| `--baseline` **and** `--write-baseline` together | recording the answer and then judging it against itself always passes, which is a gate that cannot fail |

And the distinction a subtraction gets wrong: a module that **left the
population** — its clone deleted, renamed, moved out of the owner globs, or
simply not walked — leaves the exposed set exactly as a repaired module does. It
is reported as `NOT MEASURED THIS TIME (not a fix)` and counted apart, because
adding the two together is how a campaign declares victory by losing track of
its subjects.

⚠ **Where this should run is not settled.** The baseline is a statement about one
machine's clone population, so committing it here would make this repository's
lane depend on that machine. The per-repository alternative — four lines of CI in
each repository, `modfloor --dir . --offline --floor X`, which is what the `self`
lane below already does — is a guarantee at pull-request time and costs one pull
request per repository.

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
