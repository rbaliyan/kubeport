// Package update provides interfaces and helpers for checking, downloading,
// and verifying application updates from release providers.
package update

import (
	"context"
	"crypto"
	"crypto/sha256"
	"fmt"
	"hash"
	"io"
	"strings"

	version "github.com/rbaliyan/go-version"
)

// ReleaseInfo describes a published release.
type ReleaseInfo struct {
	// Version is the semantic version of the release (e.g., "0.7.0").
	Version string
	// Tag is the git tag for the release (e.g., "v0.7.0").
	Tag string
	// URL is the release page URL.
	URL string
}

// ReleaseChecker checks for the latest release of an application.
type ReleaseChecker interface {
	LatestRelease(ctx context.Context) (*ReleaseInfo, error)
}

// ReleaseDownloader downloads release assets.
type ReleaseDownloader interface {
	// Download writes the named asset from the given release to dst.
	Download(ctx context.Context, release *ReleaseInfo, asset string, dst io.Writer) error
}

// ChecksumVerifier verifies the integrity of downloaded assets.
type ChecksumVerifier interface {
	// Verify checks that the content in r matches the expected checksum for asset.
	Verify(ctx context.Context, release *ReleaseInfo, asset string, r io.Reader) error
}

// ReleaseProvider combines checking, downloading, and verifying releases.
type ReleaseProvider interface {
	ReleaseChecker
	ReleaseDownloader
	ChecksumVerifier
}

// UpdateInfo is the result of comparing the current version against the latest.
type UpdateInfo struct {
	// Current is the running version.
	Current version.Version
	// Latest is the newest available release.
	Latest *ReleaseInfo
	// Available is true when the latest version is newer than current.
	Available bool
}

// CheckUpdate queries the checker for the latest release and compares it
// against the currently running version.
func CheckUpdate(ctx context.Context, checker ReleaseChecker) (*UpdateInfo, error) {
	current := version.Get()
	release, err := checker.LatestRelease(ctx)
	if err != nil {
		return nil, fmt.Errorf("check latest release: %w", err)
	}

	info := &UpdateInfo{
		Current: current,
		Latest:  release,
	}

	latest := parseVersion(release.Version)
	info.Available = isNewer(current, latest)
	return info, nil
}

// DownloadAndVerify downloads the named asset, verifies its checksum, and
// writes the verified content to dst. Nothing is written to dst until the
// checksum passes.
func DownloadAndVerify(ctx context.Context, provider ReleaseProvider, release *ReleaseInfo, asset string, dst io.Writer) error {
	// Download to a buffer so we can verify before writing.
	var buf strings.Builder
	if err := provider.Download(ctx, release, asset, &buf); err != nil {
		return fmt.Errorf("download %s: %w", asset, err)
	}

	content := buf.String()
	if err := provider.Verify(ctx, release, asset, strings.NewReader(content)); err != nil {
		return fmt.Errorf("verify %s: %w", asset, err)
	}

	if _, err := io.Copy(dst, strings.NewReader(content)); err != nil {
		return fmt.Errorf("write %s: %w", asset, err)
	}
	return nil
}

// HashAlgorithm returns a new hash.Hash for the given crypto.Hash.
// Defaults to SHA-256 if the algorithm is not available.
func HashAlgorithm(h crypto.Hash) hash.Hash {
	if h.Available() {
		return h.New()
	}
	return sha256.New()
}

// parseVersion extracts semver components from a version string.
func parseVersion(s string) version.Version {
	v := version.Version{Raw: s}
	s = strings.TrimPrefix(s, "v")
	parts := strings.SplitN(s, "-", 2)
	segments := strings.Split(parts[0], ".")
	if len(segments) >= 3 {
		_, _ = fmt.Sscanf(segments[0], "%d", &v.Major)
		_, _ = fmt.Sscanf(segments[1], "%d", &v.Minor)
		_, _ = fmt.Sscanf(segments[2], "%d", &v.Patch)
	}
	if len(parts) > 1 {
		v.Prefix = parts[1]
	}
	return v
}

// isNewer returns true if latest is a newer version than current.
func isNewer(current, latest version.Version) bool {
	if latest.Major != current.Major {
		return latest.Major > current.Major
	}
	if latest.Minor != current.Minor {
		return latest.Minor > current.Minor
	}
	return latest.Patch > current.Patch
}
