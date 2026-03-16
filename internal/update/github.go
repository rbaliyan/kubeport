package update

import (
	"bufio"
	"context"
	"crypto"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// GitHubProvider implements ReleaseProvider using the GitHub Releases API.
type GitHubProvider struct {
	owner string
	repo  string
	opts  githubOptions
}

type githubOptions struct {
	client   *http.Client
	checksum string
	hash     crypto.Hash
}

// compile-time check
var _ ReleaseProvider = (*GitHubProvider)(nil)

// GitHubOption configures a GitHubProvider.
type GitHubOption func(*githubOptions)

// WithHTTPClient sets a custom HTTP client.
func WithHTTPClient(c *http.Client) GitHubOption {
	return func(o *githubOptions) { o.client = c }
}

// WithChecksumFile sets the checksum filename in the release assets.
// Defaults to "checksums.txt".
func WithChecksumFile(name string) GitHubOption {
	return func(o *githubOptions) { o.checksum = name }
}

// WithHashAlgorithm sets the hash algorithm for checksum verification.
// Defaults to SHA-256.
func WithHashAlgorithm(h crypto.Hash) GitHubOption {
	return func(o *githubOptions) { o.hash = h }
}

// allowedHosts is the set of hostnames permitted for GitHub release downloads.
// This prevents SSRF by restricting requests to GitHub infrastructure only.
var allowedHosts = map[string]bool{
	"api.github.com":             true,
	"github.com":                 true,
	"objects.githubusercontent.com": true,
}

// secureHTTPClient returns an http.Client with TLS 1.2 minimum and a 60s timeout.
func secureHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 60 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				MinVersion: tls.VersionTLS12,
			},
		},
	}
}

// NewGitHubProvider creates a GitHubProvider for the given owner/repo.
func NewGitHubProvider(owner, repo string, opts ...GitHubOption) *GitHubProvider {
	o := githubOptions{
		client:   secureHTTPClient(),
		checksum: "checksums.txt",
		hash:     crypto.SHA256,
	}
	for _, opt := range opts {
		opt(&o)
	}
	return &GitHubProvider{
		owner: owner,
		repo:  repo,
		opts:  o,
	}
}

// githubRelease is the subset of the GitHub API response we need.
type githubRelease struct {
	TagName string        `json:"tag_name"`
	HTMLURL string        `json:"html_url"`
	Assets  []githubAsset `json:"assets"`
}

type githubAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// LatestRelease fetches the latest release from GitHub.
func (p *GitHubProvider) LatestRelease(ctx context.Context) (*ReleaseInfo, error) {
	rawURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", p.owner, p.repo)
	if err := validateGitHubURL(rawURL); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := p.opts.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github api: %s", resp.Status)
	}

	var release githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	ver := strings.TrimPrefix(release.TagName, "v")
	return &ReleaseInfo{
		Version: ver,
		Tag:     release.TagName,
		URL:     release.HTMLURL,
	}, nil
}

// Download streams the named asset from the release.
func (p *GitHubProvider) Download(ctx context.Context, release *ReleaseInfo, asset string, dst io.Writer) error {
	url := fmt.Sprintf("https://github.com/%s/%s/releases/download/%s/%s",
		p.owner, p.repo, release.Tag, asset)
	return p.downloadURL(ctx, url, dst)
}

// Verify downloads the checksum file and validates the asset content.
func (p *GitHubProvider) Verify(ctx context.Context, release *ReleaseInfo, asset string, r io.Reader) error {
	// Fetch checksum file.
	checksumURL := fmt.Sprintf("https://github.com/%s/%s/releases/download/%s/%s",
		p.owner, p.repo, release.Tag, p.opts.checksum)

	var checksumBuf strings.Builder
	if err := p.downloadURL(ctx, checksumURL, &checksumBuf); err != nil {
		return fmt.Errorf("download checksum file: %w", err)
	}

	// Parse checksum file to find the expected hash for asset.
	expected, err := findChecksum(checksumBuf.String(), asset)
	if err != nil {
		return err
	}

	// Compute actual hash.
	h := hashAlgorithm(p.opts.hash)
	if _, err := io.Copy(h, r); err != nil {
		return fmt.Errorf("compute hash: %w", err)
	}
	actual := hex.EncodeToString(h.Sum(nil))

	if !strings.EqualFold(actual, expected) {
		return fmt.Errorf("checksum mismatch for %s: expected %s, got %s", asset, expected, actual)
	}
	return nil
}

// validateGitHubURL checks that the URL scheme is https and the host is in
// the GitHub allowlist, preventing SSRF to internal or unexpected hosts.
func validateGitHubURL(rawURL string) error {
	// Parse manually to avoid importing net/url at the top level when not needed.
	// Use a simple prefix-and-split approach that is sufficient for our URLs.
	if !strings.HasPrefix(rawURL, "https://") {
		return fmt.Errorf("update URL must use https: %s", rawURL)
	}
	rest := strings.TrimPrefix(rawURL, "https://")
	host, _, _ := strings.Cut(rest, "/")
	// Strip port if present.
	if idx := strings.LastIndex(host, ":"); idx >= 0 {
		host = host[:idx]
	}
	if !allowedHosts[host] {
		return fmt.Errorf("update URL host %q is not in the allowed GitHub hosts list", host)
	}
	return nil
}

// downloadURL fetches a URL and writes the body to dst.
func (p *GitHubProvider) downloadURL(ctx context.Context, rawURL string, dst io.Writer) error {
	if err := validateGitHubURL(rawURL); err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}

	resp, err := p.opts.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: %s", rawURL, resp.Status)
	}

	// Limit response size to prevent OOM from compromised/redirected URLs.
	const maxResponseSize = 512 << 20 // 512 MiB
	_, err = io.Copy(dst, io.LimitReader(resp.Body, maxResponseSize))
	return err
}

// findChecksum parses a checksums.txt file (format: "hash  filename\n") and
// returns the hash for the given asset name.
func findChecksum(content, asset string) (string, error) {
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		// Format: <hash>  <filename> (two spaces) or <hash> <filename>
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		if fields[1] == asset {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("%w for %s", ErrChecksumNotFound, asset)
}
