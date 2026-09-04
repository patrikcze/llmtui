package selfupdate

import "fmt"

// ChecksumsAsset is the fixed name of the checksum manifest published with
// every release by .github/workflows/release.yml.
const ChecksumsAsset = "checksums.txt"

// supportedPlatforms is the set of GOOS/GOARCH pairs the release workflow
// publishes a desktop archive for. android is intentionally excluded: it
// ships no bundled runtime and is not a self-update target.
var supportedPlatforms = map[string]bool{
	"darwin/amd64":  true,
	"darwin/arm64":  true,
	"linux/amd64":   true,
	"linux/arm64":   true,
	"windows/amd64": true,
}

// PlatformSupported reports whether self-update publishes an archive for the
// given GOOS/GOARCH.
func PlatformSupported(goos, goarch string) bool {
	return supportedPlatforms[goos+"/"+goarch]
}

// archiveExt returns the archive extension the release workflow uses for a
// platform: zip on Windows, tar.gz elsewhere (Makefile ARCHIVE_EXT).
func archiveExt(goos string) string {
	if goos == "windows" {
		return "zip"
	}
	return "tar.gz"
}

// ExpectedAssetName returns the release asset filename for a version and
// platform, matching the Makefile's ARCHIVE_BASE pattern
// "llmtui-<version>-<goos>-<goarch>.<ext>" (version keeps its leading "v").
func ExpectedAssetName(version Version, goos, goarch string) string {
	return fmt.Sprintf("%s-%s-%s-%s.%s", RepoName, version.String(), goos, goarch, archiveExt(goos))
}

// SelectAsset finds the platform archive for the running process in a
// release and returns it along with the checksums.txt asset.
func SelectAsset(rel Release, version Version, goos, goarch string) (archive Asset, checksums Asset, err error) {
	if !PlatformSupported(goos, goarch) {
		return Asset{}, Asset{}, fmt.Errorf("self-update is not supported on %s/%s", goos, goarch)
	}
	name := ExpectedAssetName(version, goos, goarch)
	archive, ok := rel.Asset(name)
	if !ok {
		return Asset{}, Asset{}, fmt.Errorf("release %s has no asset %q", rel.TagName, name)
	}
	checksums, ok = rel.Asset(ChecksumsAsset)
	if !ok {
		return Asset{}, Asset{}, fmt.Errorf("release %s has no %s", rel.TagName, ChecksumsAsset)
	}
	return archive, checksums, nil
}
