package cli

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"

	version "github.com/rbaliyan/go-version"
	"github.com/rbaliyan/kubeport/internal/update"
)

// InstallMethod is set at build time via ldflags.
// Supported values: "github", "homebrew", "package".
// When empty, self-update is disabled (dev builds, go install).
var InstallMethod string

func (a *app) cmdUpdate(ctx context.Context, args []string) {
	sub := ""
	if len(args) > 0 {
		sub = args[0]
	}

	switch sub {
	case "check":
		a.cmdUpdateCheck(ctx)
	case "", "apply":
		a.cmdUpdateApply(ctx)
	default:
		fmt.Fprintf(os.Stderr, "Unknown update subcommand: %s\n", sub)
		fmt.Fprintln(os.Stderr, "Usage: kubeport update [check]")
		os.Exit(1)
	}
}

func (a *app) cmdUpdateCheck(ctx context.Context) {
	var cancel context.CancelFunc
	ctx, cancel = context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	provider := update.NewGitHubProvider("rbaliyan", "kubeport")
	info, err := update.CheckUpdate(ctx, provider)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error checking for updates: %v\n", err)
		return
	}

	if !info.Available {
		fmt.Printf("kubeport %s is up to date\n", currentVersionString())
		return
	}

	fmt.Printf("Update available: %s → %s\n", currentVersionString(), info.Latest.Version)
	printUpgradeHint()
}

func (a *app) cmdUpdateApply(ctx context.Context) {
	if InstallMethod != "github" {
		printUpgradeHint()
		os.Exit(1)
	}

	if err := a.doUpdateApply(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func (a *app) doUpdateApply(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	provider := update.NewGitHubProvider("rbaliyan", "kubeport")

	// Check for update.
	fmt.Print("Checking for updates... ")
	info, err := update.CheckUpdate(ctx, provider)
	if err != nil {
		return err
	}

	if !info.Available {
		fmt.Printf("already up to date (%s)\n", currentVersionString())
		return nil
	}
	fmt.Printf("found %s\n", info.Latest.Version)

	// Build asset name from goreleaser naming convention.
	asset := fmt.Sprintf("kubeport_%s_%s_%s.tar.gz",
		info.Latest.Version, runtime.GOOS, runtime.GOARCH)

	// Download and verify.
	fmt.Printf("Downloading %s... ", asset)
	tmpFile, err := os.CreateTemp("", "kubeport-update-*.tar.gz")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	defer func() { _ = os.Remove(tmpFile.Name()) }()
	defer func() { _ = tmpFile.Close() }()

	if err := update.DownloadAndVerify(ctx, provider, info.Latest, asset, tmpFile); err != nil {
		return err
	}
	fmt.Println("verified")

	// Extract binary from tar.gz.
	if _, err := tmpFile.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("seek temp file: %w", err)
	}

	binaryData, err := extractBinary(tmpFile, "kubeport")
	if err != nil {
		return fmt.Errorf("extract binary: %w", err)
	}

	// Replace the current executable.
	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate executable: %w", err)
	}
	execPath, err = filepath.EvalSymlinks(execPath)
	if err != nil {
		return fmt.Errorf("resolve executable path: %w", err)
	}

	if err := replaceBinary(execPath, binaryData); err != nil {
		return err
	}

	fmt.Printf("Updated kubeport %s → %s\n", currentVersionString(), info.Latest.Version)
	return nil
}

// printUpgradeHint prints the appropriate upgrade command for the install method.
func printUpgradeHint() {
	switch InstallMethod {
	case "homebrew":
		fmt.Println("Installed via Homebrew. Run: brew upgrade kubeport")
	case "package":
		fmt.Println("Installed via package manager. Use your package manager to upgrade.")
	case "github":
		fmt.Println("Run: kubeport update")
	default:
		fmt.Println("Self-update is not available for this build.")
		fmt.Println("Reinstall from https://github.com/rbaliyan/kubeport/releases")
	}
}

// extractBinary reads a tar.gz archive and returns the content of the named binary.
func extractBinary(r io.Reader, name string) ([]byte, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("open gzip: %w", err)
	}
	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read tar: %w", err)
		}
		// Match basename — archives often have a top-level directory.
		if filepath.Base(hdr.Name) == name && hdr.Typeflag == tar.TypeReg {
			data, err := io.ReadAll(tr)
			if err != nil {
				return nil, fmt.Errorf("read %s: %w", name, err)
			}
			return data, nil
		}
	}
	return nil, fmt.Errorf("binary %q not found in archive", name)
}

// replaceBinary atomically replaces the file at path with new content.
// It writes to a temp file in the same directory, then renames.
func replaceBinary(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".kubeport-update-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()

	// Clean up on failure.
	success := false
	defer func() {
		if !success {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Chmod(0o755); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace binary: %w", err)
	}
	success = true
	return nil
}

func currentVersionString() string {
	v := version.Get()
	if v.Raw != "" {
		return v.Raw
	}
	return "dev"
}
