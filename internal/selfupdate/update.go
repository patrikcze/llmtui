package selfupdate

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// UpdatePlan is the fully-resolved intent of a `self update`, computed
// without touching the live installation.
type UpdatePlan struct {
	Build           BuildInfo
	Current         Version
	IsDevBuild      bool
	Latest          Version
	Release         Release
	Archive         Asset
	Checksums       Asset
	Target          Target
	UpdateAvailable bool // Latest strictly newer than Current
}

// AlreadyCurrent reports whether the plan would not change the version.
func (p *UpdatePlan) AlreadyCurrent() bool {
	return !p.IsDevBuild && p.Latest.Compare(p.Current) <= 0
}

// PlanUpdate performs release discovery and asset selection for the running
// process. It makes network calls but no filesystem changes.
func PlanUpdate(ctx context.Context, client *Client, build BuildInfo, target Target, includePrerelease bool) (*UpdatePlan, error) {
	if !PlatformSupported(build.OS, build.Arch) {
		return nil, fmt.Errorf("self-update is not supported on %s/%s (network providers still work; build from source)", build.OS, build.Arch)
	}
	plan := &UpdatePlan{Build: build, Target: target, IsDevBuild: IsDevBuild(build.Version)}
	if !plan.IsDevBuild {
		v, err := ParseVersion(build.Version)
		if err != nil {
			plan.IsDevBuild = true
		} else {
			plan.Current = v
		}
	}

	rel, err := client.LatestStableRelease(ctx, includePrerelease)
	if err != nil {
		return nil, err
	}
	latest, err := ParseVersion(rel.TagName)
	if err != nil {
		return nil, fmt.Errorf("release tag %q is not a version: %w", rel.TagName, err)
	}
	plan.Release = rel
	plan.Latest = latest
	plan.UpdateAvailable = plan.IsDevBuild || latest.Compare(plan.Current) > 0

	archive, checksums, err := SelectAsset(rel, latest, build.OS, build.Arch)
	if err != nil {
		return nil, err
	}
	plan.Archive = archive
	plan.Checksums = checksums
	return plan, nil
}

// ExecuteUpdate downloads the planned release, verifies it against the
// official checksums.txt, extracts it safely, validates the staged binary,
// and installs it transactionally. The running installation is untouched
// until every prior step has succeeded.
func ExecuteUpdate(ctx context.Context, client *Client, plan *UpdatePlan, progress io.Writer) error {
	logf := func(format string, a ...any) {
		if progress != nil {
			fmt.Fprintf(progress, format+"\n", a...)
		}
	}

	// Preflight write permission before any download.
	if err := ensureWritablePrefix(plan.Target); err != nil {
		return err
	}

	work, err := os.MkdirTemp("", "llmtui-update-")
	if err != nil {
		return fmt.Errorf("create work dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(work) }()

	logf("Downloading %s ...", plan.Archive.Name)
	archivePath := filepath.Join(work, plan.Archive.Name)
	dl, err := DownloadAsset(ctx, client.HTTP, plan.Archive, archivePath)
	if err != nil {
		return err
	}

	logf("Downloading %s ...", plan.Checksums.Name)
	checksumsPath := filepath.Join(work, ChecksumsAsset)
	if _, err := DownloadAsset(ctx, client.HTTP, plan.Checksums, checksumsPath); err != nil {
		return err
	}

	cf, err := os.Open(checksumsPath)
	if err != nil {
		return err
	}
	sums, err := ParseChecksums(cf)
	_ = cf.Close()
	if err != nil {
		return err
	}
	want, ok := sums.For(plan.Archive.Name)
	if !ok {
		return fmt.Errorf("%s has no entry for %s", ChecksumsAsset, plan.Archive.Name)
	}
	if err := VerifyFileChecksum(archivePath, want); err != nil {
		return err // aborts before extraction; installation untouched
	}
	if dl.SHA256 != want {
		return fmt.Errorf("checksum mismatch for %s", plan.Archive.Name)
	}
	logf("Verified SHA-256 against %s.", ChecksumsAsset)

	payload, err := ExtractRelease(archivePath, plan.Target.Prefix, plan.Build.OS)
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(payload.Dir) }()

	logf("Installing %s to %s ...", plan.Latest, plan.Target.BinPath)
	requireRuntime := PlatformSupported(plan.Build.OS, plan.Build.Arch)
	if err := payload.Install(plan.Target, InstallMeta{
		Version: plan.Latest.String(),
		Asset:   plan.Archive.Name,
		By:      "self update",
	}, requireRuntime); err != nil {
		return err
	}
	return nil
}
