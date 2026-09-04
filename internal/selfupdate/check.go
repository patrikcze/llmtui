package selfupdate

import (
	"context"
	"fmt"
)

// CheckResult is the read-only outcome of `self check`.
type CheckResult struct {
	CurrentRaw      string
	CurrentVersion  Version // zero value when IsDevBuild
	IsDevBuild      bool
	LatestVersion   Version
	LatestRelease   Release
	OS, Arch        string
	PlatformOK      bool
	ExpectedAsset   string // best-effort; "" when the platform is unsupported
	UpdateAvailable bool
}

// Check queries GitHub for the latest release and compares it to the running
// build. It performs no filesystem modification.
func Check(ctx context.Context, client *Client, build BuildInfo, includePrerelease bool) (CheckResult, error) {
	res := CheckResult{
		CurrentRaw: build.Version,
		OS:         build.OS,
		Arch:       build.Arch,
		IsDevBuild: IsDevBuild(build.Version),
		PlatformOK: PlatformSupported(build.OS, build.Arch),
	}
	if !res.IsDevBuild {
		if v, err := ParseVersion(build.Version); err == nil {
			res.CurrentVersion = v
		} else {
			res.IsDevBuild = true
		}
	}

	rel, err := client.LatestStableRelease(ctx, includePrerelease)
	if err != nil {
		return res, err
	}
	latest, err := ParseVersion(rel.TagName)
	if err != nil {
		return res, fmt.Errorf("latest release tag %q is not a version: %w", rel.TagName, err)
	}
	res.LatestRelease = rel
	res.LatestVersion = latest

	if res.PlatformOK {
		res.ExpectedAsset = ExpectedAssetName(latest, build.OS, build.Arch)
	}
	if !res.IsDevBuild {
		res.UpdateAvailable = latest.Compare(res.CurrentVersion) > 0
	}
	return res, nil
}
