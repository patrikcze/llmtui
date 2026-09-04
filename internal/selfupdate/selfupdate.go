// Package selfupdate implements llmtui's self-management commands: checking
// GitHub Releases for a newer build, installing the current build into a
// managed location, and performing a verified, staged, transactional
// self-update.
//
// Design invariants (see docs/self-management.md):
//
//   - The running executable is never destroyed until a replacement release
//     has been downloaded, its SHA-256 verified against the official
//     checksums.txt, safely extracted, validated, and staged on the target
//     filesystem.
//   - All destinations are chosen by llmtui code. GitHub JSON and archive
//     contents never select a filesystem path.
//   - Network access is HTTPS-only, bounded, explicit, and never required for
//     the read-only commands `self path` (no network at all) beyond what the
//     user invokes.
//   - The binary and its executable-relative bundled llama.cpp runtime are
//     replaced together as one release payload, preserving the guarantees of
//     internal/runtime's resolver.
//
// The package is deliberately independent of internal/cli: release discovery,
// download, verification, extraction and the filesystem transaction are all
// unit-testable without invoking Cobra.
package selfupdate

import "runtime"

// Repo is the single source of truth for the upstream repository that
// self-update trusts. It is not configurable: a release payload must never be
// able to redirect llmtui at a different repository.
const (
	RepoOwner = "patrikcze"
	RepoName  = "llmtui"
)

// userAgent is sent on every GitHub request. GitHub rejects API requests with
// no User-Agent.
const userAgent = "llmtui-selfupdate/1 (+https://github.com/" + RepoOwner + "/" + RepoName + ")"

// BuildInfo describes the running binary, as injected via -ldflags into
// package main and threaded through the CLI.
type BuildInfo struct {
	Version string // e.g. "v1.0.22", or "dev" for a source build
	Commit  string
	Date    string
	OS      string // runtime.GOOS at build time of this process
	Arch    string // runtime.GOARCH
}

// CurrentBuild returns the BuildInfo for this process. os/arch always reflect
// the running process: a self-update can only replace the binary with one for
// the same platform.
func CurrentBuild(version, commit, date string) BuildInfo {
	return BuildInfo{
		Version: version,
		Commit:  commit,
		Date:    date,
		OS:      runtime.GOOS,
		Arch:    runtime.GOARCH,
	}
}
