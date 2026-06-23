package main

import (
	"fmt"
	"runtime"
)

// Build-time version info, injected via the linker:
//
//	go build -ldflags "-X main.version=… -X main.commit=… -X main.buildDate=…"
//
// The Makefile (build / docker-build / publish) and the Dockerfile set these
// from `git describe` / `git rev-parse` / the build timestamp. A plain `go build`
// leaves the defaults, which read as a dev build.
var (
	version   = "dev"
	commit    = "none"
	buildDate = "unknown"
)

// versionString is the one-line version banner.
func versionString() string {
	return fmt.Sprintf("agentbox %s (commit %s, built %s, %s)", version, commit, buildDate, runtime.Version())
}
