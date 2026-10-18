module github.com/go-fleettools/modfloor

// ⛔ 1.27.2 AND NOT 1.27.1, which is the whole subject of this tool: go1.27.1
// has eight advisories reachable from an ordinary module through net/http and
// the standard library, all fixed in go1.27.2. A consumer running
// `go install` with GOTOOLCHAIN=local honours THIS line, so a tool that
// measures the declared floor must not ship a bad one.
//
// The negative control, measured on a sibling repository: with the directive at
// 1.27.1 and toolchain 1.27.1, govulncheck reports 8; with the directive at
// 1.27.2, `GOTOOLCHAIN=local go build` on 1.27.1 REFUSES — "go.mod requires
// go >= 1.27.2" — instead of silently building the vulnerable binary.
go 1.27.2

require (
	github.com/spf13/cobra v1.10.2
	golang.org/x/mod v0.42.0
)

require (
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/spf13/pflag v1.0.9 // indirect
)
