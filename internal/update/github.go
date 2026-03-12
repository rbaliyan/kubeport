package update

import (
	"bufio"
	"context"
	"crypto"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
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

// NewGitHubProvider creates a GitHubProvider for the given owner/repo.
func NewGitHubProvider(owner, repo string, opts ...GitHubOption) *GitHubProvider {
	o := githubOptions{
		client:   http.DefaultClient,
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
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", p.owner, p.repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
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

// downloadURL fetches a URL and writes the body to dst.
func (p *GitHubProvider) downloadURL(ctx context.Context, url string, dst io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}

	resp, err := p.opts.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: %s", url, resp.Status)
	}

	_, err = io.Copy(dst, resp.Body)
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
