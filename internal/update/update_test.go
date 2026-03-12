package update

import (
	"context"
	"crypto"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	version "github.com/rbaliyan/go-version"
)

func TestParseVersion(t *testing.T) {
	tests := []struct {
		input  string
		major  int
		minor  int
		patch  int
		prefix string
	}{
		{"0.6.1", 0, 6, 1, ""},
		{"v0.6.1", 0, 6, 1, ""},
		{"1.2.3", 1, 2, 3, ""},
		{"1.2.3-beta.1", 1, 2, 3, "beta.1"},
		{"v10.20.30", 10, 20, 30, ""},
		{"v1.0.0-rc1", 1, 0, 0, "rc1"},
		{"", 0, 0, 0, ""},
		{"invalid", 0, 0, 0, ""},
		{"1.2", 0, 0, 0, ""},
		{"v1", 0, 0, 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			v := parseVersion(tt.input)
			if v.Major != tt.major || v.Minor != tt.minor || v.Patch != tt.patch {
				t.Errorf("parseVersion(%q) = %d.%d.%d, want %d.%d.%d",
					tt.input, v.Major, v.Minor, v.Patch, tt.major, tt.minor, tt.patch)
			}
			if v.Prefix != tt.prefix {
				t.Errorf("parseVersion(%q).Prefix = %q, want %q", tt.input, v.Prefix, tt.prefix)
			}
			if v.Raw != tt.input {
				t.Errorf("parseVersion(%q).Raw = %q", tt.input, v.Raw)
			}
		})
	}
}

func TestIsNewer(t *testing.T) {
	tests := []struct {
		current string
		latest  string
		want    bool
	}{
		{"0.6.1", "0.6.2", true},
		{"0.6.1", "0.7.0", true},
		{"0.6.1", "1.0.0", true},
		{"0.0.0", "0.0.1", true},
		{"0.6.1", "0.6.1", false},
		{"0.6.2", "0.6.1", false},
		{"1.0.0", "0.9.9", false},
		{"2.0.0", "1.99.99", false},
		{"0.0.0", "0.0.0", false},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s→%s", tt.current, tt.latest), func(t *testing.T) {
			got := isNewer(parseVersion(tt.current), parseVersion(tt.latest))
			if got != tt.want {
				t.Errorf("isNewer(%s, %s) = %v, want %v", tt.current, tt.latest, got, tt.want)
			}
		})
	}
}

func TestFindChecksum(t *testing.T) {
	content := `abc123def456  kubeport_0.7.0_linux_amd64.tar.gz
789xyz000111  kubeport_0.7.0_darwin_arm64.tar.gz
`
	t.Run("found first entry", func(t *testing.T) {
		got, err := findChecksum(content, "kubeport_0.7.0_linux_amd64.tar.gz")
		if err != nil {
			t.Fatal(err)
		}
		if got != "abc123def456" {
			t.Errorf("got %q, want %q", got, "abc123def456")
		}
	})

	t.Run("found second entry", func(t *testing.T) {
		got, err := findChecksum(content, "kubeport_0.7.0_darwin_arm64.tar.gz")
		if err != nil {
			t.Fatal(err)
		}
		if got != "789xyz000111" {
			t.Errorf("got %q, want %q", got, "789xyz000111")
		}
	})

	t.Run("not found returns sentinel", func(t *testing.T) {
		_, err := findChecksum(content, "kubeport_0.7.0_windows_amd64.tar.gz")
		if !errors.Is(err, ErrChecksumNotFound) {
			t.Errorf("expected ErrChecksumNotFound, got: %v", err)
		}
	})

	t.Run("empty content", func(t *testing.T) {
		_, err := findChecksum("", "anything")
		if !errors.Is(err, ErrChecksumNotFound) {
			t.Errorf("expected ErrChecksumNotFound, got: %v", err)
		}
	})

	t.Run("blank lines and whitespace", func(t *testing.T) {
		c := "\n\n  \nabc123  myfile.tar.gz\n  \n"
		got, err := findChecksum(c, "myfile.tar.gz")
		if err != nil {
			t.Fatal(err)
		}
		if got != "abc123" {
			t.Errorf("got %q, want %q", got, "abc123")
		}
	})

	t.Run("malformed lines skipped", func(t *testing.T) {
		c := "not-a-checksum-line\nabc123  myfile.tar.gz\nsingle-field\n"
		got, err := findChecksum(c, "myfile.tar.gz")
		if err != nil {
			t.Fatal(err)
		}
		if got != "abc123" {
			t.Errorf("got %q, want %q", got, "abc123")
		}
	})

	t.Run("single space separator", func(t *testing.T) {
		c := "abc123 myfile.tar.gz\n"
		got, err := findChecksum(c, "myfile.tar.gz")
		if err != nil {
			t.Fatal(err)
		}
		if got != "abc123" {
			t.Errorf("got %q, want %q", got, "abc123")
		}
	})
}

func TestHashAlgorithm(t *testing.T) {
	t.Run("sha256", func(t *testing.T) {
		h := hashAlgorithm(crypto.SHA256)
		_, _ = h.Write([]byte("test"))
		got := hex.EncodeToString(h.Sum(nil))
		want := "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
		if got != want {
			t.Errorf("SHA256 hash mismatch: got %s", got)
		}
	})

	t.Run("sha512", func(t *testing.T) {
		h := hashAlgorithm(crypto.SHA512)
		_, _ = h.Write([]byte("test"))
		got := hex.EncodeToString(h.Sum(nil))
		expected := sha512.Sum512([]byte("test"))
		want := hex.EncodeToString(expected[:])
		if got != want {
			t.Errorf("SHA512 hash mismatch: got %s", got)
		}
	})

	t.Run("unavailable falls back to sha256", func(t *testing.T) {
		h := hashAlgorithm(crypto.Hash(0)) // invalid hash
		_, _ = h.Write([]byte("test"))
		got := hex.EncodeToString(h.Sum(nil))
		expected := sha256.Sum256([]byte("test"))
		want := hex.EncodeToString(expected[:])
		if got != want {
			t.Errorf("fallback hash mismatch: got %s, want %s", got, want)
		}
	})
}

// --- NewGitHubProvider ---

func TestNewGitHubProviderDefaults(t *testing.T) {
	p := NewGitHubProvider("owner", "repo")
	if p.owner != "owner" {
		t.Errorf("owner = %q, want %q", p.owner, "owner")
	}
	if p.repo != "repo" {
		t.Errorf("repo = %q, want %q", p.repo, "repo")
	}
	if p.opts.client != http.DefaultClient {
		t.Error("expected default http client")
	}
	if p.opts.checksum != "checksums.txt" {
		t.Errorf("checksum = %q, want %q", p.opts.checksum, "checksums.txt")
	}
	if p.opts.hash != crypto.SHA256 {
		t.Errorf("hash = %v, want SHA256", p.opts.hash)
	}
}

func TestNewGitHubProviderOptions(t *testing.T) {
	custom := &http.Client{}
	p := NewGitHubProvider("o", "r",
		WithHTTPClient(custom),
		WithChecksumFile("sha256sums.txt"),
		WithHashAlgorithm(crypto.SHA512),
	)
	if p.opts.client != custom {
		t.Error("expected custom http client")
	}
	if p.opts.checksum != "sha256sums.txt" {
		t.Errorf("checksum = %q, want %q", p.opts.checksum, "sha256sums.txt")
	}
	if p.opts.hash != crypto.SHA512 {
		t.Errorf("hash = %v, want SHA512", p.opts.hash)
	}
}

// --- GitHubProvider.LatestRelease ---

func TestGitHubProviderLatestRelease(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/rbaliyan/kubeport/releases/latest" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Accept") != "application/vnd.github+json" {
			t.Error("missing Accept header")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"tag_name": "v0.7.0",
			"html_url": "https://github.com/rbaliyan/kubeport/releases/tag/v0.7.0",
			"assets": [{"name": "kubeport_0.7.0_darwin_arm64.tar.gz", "browser_download_url": "https://example.com/download"}]
		}`)
	}))
	defer srv.Close()

	p := NewGitHubProvider("rbaliyan", "kubeport", WithHTTPClient(testClient(srv)))
	release, err := p.LatestRelease(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if release.Version != "0.7.0" {
		t.Errorf("version = %q, want %q", release.Version, "0.7.0")
	}
	if release.Tag != "v0.7.0" {
		t.Errorf("tag = %q, want %q", release.Tag, "v0.7.0")
	}
	if release.URL != "https://github.com/rbaliyan/kubeport/releases/tag/v0.7.0" {
		t.Errorf("url = %q", release.URL)
	}
}

func TestGitHubProviderLatestReleaseNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	p := NewGitHubProvider("rbaliyan", "kubeport", WithHTTPClient(testClient(srv)))
	_, err := p.LatestRelease(context.Background())
	if err == nil {
		t.Fatal("expected error for 404 response")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error should contain status code: %v", err)
	}
}

func TestGitHubProviderLatestReleaseBadJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{not valid json`)
	}))
	defer srv.Close()

	p := NewGitHubProvider("rbaliyan", "kubeport", WithHTTPClient(testClient(srv)))
	_, err := p.LatestRelease(context.Background())
	if err == nil {
		t.Fatal("expected error for bad JSON")
	}
	if !strings.Contains(err.Error(), "decode") {
		t.Errorf("expected decode error, got: %v", err)
	}
}

func TestGitHubProviderLatestReleaseCanceled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		select {}
	}))
	defer srv.Close()

	p := NewGitHubProvider("rbaliyan", "kubeport", WithHTTPClient(testClient(srv)))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := p.LatestRelease(ctx)
	if err == nil {
		t.Fatal("expected error for canceled context")
	}
}

func TestGitHubProviderLatestReleaseNoVPrefix(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"tag_name": "1.0.0", "html_url": "https://example.com", "assets": []}`)
	}))
	defer srv.Close()

	p := NewGitHubProvider("o", "r", WithHTTPClient(testClient(srv)))
	release, err := p.LatestRelease(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if release.Version != "1.0.0" {
		t.Errorf("version = %q, want %q", release.Version, "1.0.0")
	}
	if release.Tag != "1.0.0" {
		t.Errorf("tag = %q, want %q", release.Tag, "1.0.0")
	}
}

// --- GitHubProvider.Download ---

func TestGitHubProviderDownload(t *testing.T) {
	body := "binary-content-bytes"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wantPath := "/rbaliyan/kubeport/releases/download/v0.7.0/kubeport_0.7.0_darwin_arm64.tar.gz"
		if r.URL.Path != wantPath {
			t.Errorf("path = %q, want %q", r.URL.Path, wantPath)
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, body)
	}))
	defer srv.Close()

	p := NewGitHubProvider("rbaliyan", "kubeport", WithHTTPClient(testClient(srv)))
	release := &ReleaseInfo{Version: "0.7.0", Tag: "v0.7.0"}

	var buf strings.Builder
	err := p.Download(context.Background(), release, "kubeport_0.7.0_darwin_arm64.tar.gz", &buf)
	if err != nil {
		t.Fatal(err)
	}
	if buf.String() != body {
		t.Errorf("got %q, want %q", buf.String(), body)
	}
}

func TestGitHubProviderDownloadNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	p := NewGitHubProvider("o", "r", WithHTTPClient(testClient(srv)))
	release := &ReleaseInfo{Version: "1.0.0", Tag: "v1.0.0"}

	var buf strings.Builder
	err := p.Download(context.Background(), release, "asset.tar.gz", &buf)
	if err == nil {
		t.Fatal("expected error for 403")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("error should contain status code: %v", err)
	}
}

func TestGitHubProviderDownloadCanceled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		select {}
	}))
	defer srv.Close()

	p := NewGitHubProvider("o", "r", WithHTTPClient(testClient(srv)))
	release := &ReleaseInfo{Version: "1.0.0", Tag: "v1.0.0"}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var buf strings.Builder
	err := p.Download(ctx, release, "asset.tar.gz", &buf)
	if err == nil {
		t.Fatal("expected error for canceled context")
	}
}

// --- GitHubProvider.Verify ---

func TestGitHubProviderVerify(t *testing.T) {
	assetContent := "binary-content-here"
	h := sha256.Sum256([]byte(assetContent))
	checksum := hex.EncodeToString(h[:])

	checksumFile := fmt.Sprintf("%s  kubeport_0.7.0_darwin_arm64.tar.gz\n%s  kubeport_0.7.0_linux_amd64.tar.gz\n",
		checksum, "0000000000000000000000000000000000000000000000000000000000000000")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/checksums.txt") {
			fmt.Fprint(w, checksumFile)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	p := NewGitHubProvider("rbaliyan", "kubeport", WithHTTPClient(testClient(srv)))
	release := &ReleaseInfo{Version: "0.7.0", Tag: "v0.7.0"}

	t.Run("valid checksum", func(t *testing.T) {
		err := p.Verify(context.Background(), release, "kubeport_0.7.0_darwin_arm64.tar.gz",
			strings.NewReader(assetContent))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("invalid checksum", func(t *testing.T) {
		err := p.Verify(context.Background(), release, "kubeport_0.7.0_darwin_arm64.tar.gz",
			strings.NewReader("tampered-content"))
		if err == nil {
			t.Fatal("expected checksum mismatch error")
		}
		if !strings.Contains(err.Error(), "checksum mismatch") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("asset not in checksum file", func(t *testing.T) {
		err := p.Verify(context.Background(), release, "kubeport_0.7.0_windows_amd64.tar.gz",
			strings.NewReader(assetContent))
		if err == nil {
			t.Fatal("expected error for missing asset in checksum file")
		}
		if !errors.Is(err, ErrChecksumNotFound) {
			t.Errorf("expected ErrChecksumNotFound, got: %v", err)
		}
	})
}

func TestGitHubProviderVerifyChecksumFileNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	p := NewGitHubProvider("o", "r", WithHTTPClient(testClient(srv)))
	release := &ReleaseInfo{Version: "1.0.0", Tag: "v1.0.0"}

	err := p.Verify(context.Background(), release, "asset.tar.gz", strings.NewReader("data"))
	if err == nil {
		t.Fatal("expected error when checksum file not found")
	}
	if !strings.Contains(err.Error(), "checksum file") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestGitHubProviderVerifyCustomChecksumFile(t *testing.T) {
	content := "test-data"
	h := sha256.Sum256([]byte(content))
	checksum := hex.EncodeToString(h[:])

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/sha256sums.txt") {
			fmt.Fprintf(w, "%s  asset.tar.gz\n", checksum)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	p := NewGitHubProvider("o", "r",
		WithHTTPClient(testClient(srv)),
		WithChecksumFile("sha256sums.txt"),
	)
	release := &ReleaseInfo{Version: "1.0.0", Tag: "v1.0.0"}

	err := p.Verify(context.Background(), release, "asset.tar.gz", strings.NewReader(content))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGitHubProviderVerifySHA512(t *testing.T) {
	content := "test-data-512"
	h := sha512.Sum512([]byte(content))
	checksum := hex.EncodeToString(h[:])

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/checksums.txt") {
			fmt.Fprintf(w, "%s  asset.tar.gz\n", checksum)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	p := NewGitHubProvider("o", "r",
		WithHTTPClient(testClient(srv)),
		WithHashAlgorithm(crypto.SHA512),
	)
	release := &ReleaseInfo{Version: "1.0.0", Tag: "v1.0.0"}

	err := p.Verify(context.Background(), release, "asset.tar.gz", strings.NewReader(content))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// --- CheckUpdate ---

func TestCheckUpdateAvailable(t *testing.T) {
	current := parseVersion("0.6.1")
	checker := &mockChecker{release: &ReleaseInfo{
		Version: "0.7.0",
		Tag:     "v0.7.0",
		URL:     "https://example.com",
	}}

	info, err := CheckUpdate(context.Background(), checker, current)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Available {
		t.Error("expected update to be available")
	}
	if info.Latest.Version != "0.7.0" {
		t.Errorf("latest = %q, want %q", info.Latest.Version, "0.7.0")
	}
}

func TestCheckUpdateNotAvailable(t *testing.T) {
	current := parseVersion("0.7.0")
	checker := &mockChecker{release: &ReleaseInfo{
		Version: "0.7.0",
		Tag:     "v0.7.0",
	}}

	info, err := CheckUpdate(context.Background(), checker, current)
	if err != nil {
		t.Fatal(err)
	}
	if info.Available {
		t.Error("expected no update available")
	}
}

func TestCheckUpdateOlderRemote(t *testing.T) {
	current := parseVersion("1.0.0")
	checker := &mockChecker{release: &ReleaseInfo{
		Version: "0.6.1",
		Tag:     "v0.6.1",
	}}

	info, err := CheckUpdate(context.Background(), checker, current)
	if err != nil {
		t.Fatal(err)
	}
	if info.Available {
		t.Error("expected no update when remote is older")
	}
}

func TestCheckUpdateCheckerError(t *testing.T) {
	current := parseVersion("0.6.1")
	checker := &mockChecker{err: errors.New("network error")}

	_, err := CheckUpdate(context.Background(), checker, current)
	if err == nil {
		t.Fatal("expected error from checker")
	}
	if !strings.Contains(err.Error(), "network error") {
		t.Errorf("expected wrapped error, got: %v", err)
	}
}

// --- DownloadAndVerify ---

func TestDownloadAndVerify(t *testing.T) {
	content := "the-binary"
	h := sha256.Sum256([]byte(content))
	checksum := hex.EncodeToString(h[:])

	p := &mockProvider{
		downloadContent: content,
		checksumContent: fmt.Sprintf("%s  asset.tar.gz", checksum),
	}
	release := &ReleaseInfo{Version: "1.0.0", Tag: "v1.0.0"}

	var buf strings.Builder
	err := DownloadAndVerify(context.Background(), p, release, "asset.tar.gz", &buf)
	if err != nil {
		t.Fatal(err)
	}
	if buf.String() != content {
		t.Errorf("got %q, want %q", buf.String(), content)
	}
}

func TestDownloadAndVerifyChecksumMismatch(t *testing.T) {
	p := &mockProvider{
		downloadContent: "actual-content",
		checksumContent: "0000000000000000000000000000000000000000000000000000000000000000  asset.tar.gz",
	}
	release := &ReleaseInfo{Version: "1.0.0", Tag: "v1.0.0"}

	var buf strings.Builder
	err := DownloadAndVerify(context.Background(), p, release, "asset.tar.gz", &buf)
	if err == nil {
		t.Fatal("expected checksum mismatch error")
	}
	if !strings.Contains(err.Error(), "verify") {
		t.Errorf("expected verify error, got: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("expected empty buffer on checksum failure, got %d bytes", buf.Len())
	}
}

func TestDownloadAndVerifyDownloadError(t *testing.T) {
	p := &mockProvider{
		downloadErr: errors.New("connection refused"),
	}
	release := &ReleaseInfo{Version: "1.0.0", Tag: "v1.0.0"}

	var buf strings.Builder
	err := DownloadAndVerify(context.Background(), p, release, "asset.tar.gz", &buf)
	if err == nil {
		t.Fatal("expected download error")
	}
	if !strings.Contains(err.Error(), "download") {
		t.Errorf("expected download error, got: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("expected empty buffer on download failure, got %d bytes", buf.Len())
	}
}

func TestDownloadAndVerifyAssetNotInChecksums(t *testing.T) {
	content := "the-binary"
	p := &mockProvider{
		downloadContent: content,
		checksumContent: "abc123  other-asset.tar.gz",
	}
	release := &ReleaseInfo{Version: "1.0.0", Tag: "v1.0.0"}

	var buf strings.Builder
	err := DownloadAndVerify(context.Background(), p, release, "asset.tar.gz", &buf)
	if err == nil {
		t.Fatal("expected error when asset not in checksums")
	}
	if buf.Len() != 0 {
		t.Errorf("expected empty buffer on verify failure, got %d bytes", buf.Len())
	}
}

// --- helpers ---

// testClient creates an http.Client that redirects all requests to the test server.
func testClient(srv *httptest.Server) *http.Client {
	client := srv.Client()
	origTransport := client.Transport
	client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		req.URL.Scheme = "http"
		req.URL.Host = srv.Listener.Addr().String()
		if origTransport != nil {
			return origTransport.RoundTrip(req)
		}
		return http.DefaultTransport.RoundTrip(req)
	})
	return client
}

// roundTripFunc adapts a function to http.RoundTripper.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// mockChecker implements ReleaseChecker for testing.
type mockChecker struct {
	release *ReleaseInfo
	err     error
}

func (m *mockChecker) LatestRelease(context.Context) (*ReleaseInfo, error) {
	return m.release, m.err
}

// mockProvider implements ReleaseProvider for testing.
type mockProvider struct {
	downloadContent string
	downloadErr     error
	checksumContent string
}

var _ version.Version // ensure go-version is still imported for compile check

func (m *mockProvider) LatestRelease(context.Context) (*ReleaseInfo, error) {
	return &ReleaseInfo{Version: "1.0.0", Tag: "v1.0.0"}, nil
}

func (m *mockProvider) Download(_ context.Context, _ *ReleaseInfo, _ string, dst io.Writer) error {
	if m.downloadErr != nil {
		return m.downloadErr
	}
	_, err := io.WriteString(dst, m.downloadContent)
	return err
}

func (m *mockProvider) Verify(_ context.Context, _ *ReleaseInfo, asset string, r io.Reader) error {
	expected, err := findChecksum(m.checksumContent, asset)
	if err != nil {
		return err
	}
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return err
	}
	actual := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(actual, expected) {
		return fmt.Errorf("checksum mismatch: expected %s, got %s", expected, actual)
	}
	return nil
}
