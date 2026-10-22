module github.com/go-fleettools/modfloor

// ⛔ 1.27.3 AND NOT 1.27.2, for the reason this tool exists: a declared floor
// is what a consumer running `go install` with GOTOOLCHAIN=local actually
// honours, so a tool that measures other modules' floors must not ship a bad
// one. The number has now moved twice for exactly this, and the record of both
// is the point:
//
//   1.27.1 -> 1.27.2   eight advisories reachable from an ordinary module
//                      through net/http and the standard library.
//   1.27.2 -> 1.27.3   GO-2026-6809 (CVE-2026-111943), "Distrust compromised
//                      intermediate certificates in crypto/x509", published
//                      2026-10-21, fixed in go1.26.10 and go1.27.3.
//
// Measured here on 2026-10-22, same tree, same govulncheck, GOOS=linux
// GOARCH=amd64, the directive the only variable:
//
//   go 1.27.2  cmd.go:105:12: modfloor.RootCmd calls pflag.FlagSet.BoolVarP,
//              which eventually calls x509.Certificate.Verify
//              -> affected by 1 vulnerability from the Go standard library
//   go 1.27.3  No vulnerabilities found.
//
// The negative control, measured on a sibling repository when the floor was
// 1.27.2: with the directive at 1.27.1 and toolchain 1.27.1, govulncheck
// reports 8; with the directive at 1.27.2, `GOTOOLCHAIN=local go build` on
// 1.27.1 REFUSES — "go.mod requires go >= 1.27.2" — instead of silently
// building the vulnerable binary.
go 1.27.3

require (
	github.com/spf13/cobra v1.10.2
	golang.org/x/mod v0.42.0
)

require (
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/spf13/pflag v1.0.9 // indirect
)
