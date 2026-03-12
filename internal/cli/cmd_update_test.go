package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func TestExtractBinary(t *testing.T) {
	t.Run("flat archive", func(t *testing.T) {
		buf := createTarGz(t, map[string]string{
			"kubeport": "binary-content",
			"README":   "readme text",
		})
		data, err := extractBinary(buf, "kubeport")
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != "binary-content" {
			t.Errorf("got %q, want %q", string(data), "binary-content")
		}
	})

	t.Run("nested directory", func(t *testing.T) {
		buf := createTarGz(t, map[string]string{
			"kubeport_0.7.0_darwin_arm64/kubeport": "nested-binary",
			"kubeport_0.7.0_darwin_arm64/README":   "readme",
		})
		data, err := extractBinary(buf, "kubeport")
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != "nested-binary" {
			t.Errorf("got %q, want %q", string(data), "nested-binary")
		}
	})

	t.Run("deeply nested", func(t *testing.T) {
		buf := createTarGz(t, map[string]string{
			"a/b/c/kubeport": "deep-binary",
		})
		data, err := extractBinary(buf, "kubeport")
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != "deep-binary" {
			t.Errorf("got %q, want %q", string(data), "deep-binary")
		}
	})

	t.Run("binary not found", func(t *testing.T) {
		buf := createTarGz(t, map[string]string{
			"other-binary": "content",
			"README":       "readme",
		})
		_, err := extractBinary(buf, "kubeport")
		if err == nil {
			t.Fatal("expected error when binary not found")
		}
		if got := err.Error(); got != `binary "kubeport" not found in archive` {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("empty archive", func(t *testing.T) {
		buf := createTarGz(t, map[string]string{})
		_, err := extractBinary(buf, "kubeport")
		if err == nil {
			t.Fatal("expected error for empty archive")
		}
	})

	t.Run("invalid gzip", func(t *testing.T) {
		_, err := extractBinary(bytes.NewReader([]byte("not gzip")), "kubeport")
		if err == nil {
			t.Fatal("expected error for invalid gzip")
		}
	})
}

func TestReplaceBinary(t *testing.T) {
	t.Run("replaces file", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "kubeport")

		// Create original file.
		if err := os.WriteFile(path, []byte("old-binary"), 0o755); err != nil {
			t.Fatal(err)
		}

		// Replace.
		if err := replaceBinary(path, []byte("new-binary")); err != nil {
			t.Fatal(err)
		}

		// Verify content.
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != "new-binary" {
			t.Errorf("got %q, want %q", string(data), "new-binary")
		}

		// Verify permissions.
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o755 {
			t.Errorf("permissions = %o, want 755", info.Mode().Perm())
		}
	})

	t.Run("creates new file", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "kubeport")

		if err := replaceBinary(path, []byte("new-binary")); err != nil {
			t.Fatal(err)
		}

		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != "new-binary" {
			t.Errorf("got %q, want %q", string(data), "new-binary")
		}
	})

	t.Run("bad directory", func(t *testing.T) {
		err := replaceBinary("/nonexistent/dir/kubeport", []byte("data"))
		if err == nil {
			t.Fatal("expected error for nonexistent directory")
		}
	})

	t.Run("no temp file left on failure", func(t *testing.T) {
		dir := t.TempDir()
		// Make directory read-only so rename fails (write temp succeeds, rename fails).
		path := filepath.Join(dir, "subdir", "kubeport")

		// This should fail because subdir doesn't exist.
		err := replaceBinary(path, []byte("data"))
		if err == nil {
			t.Fatal("expected error")
		}

		// No temp files should be left in dir.
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if filepath.Ext(e.Name()) != "" {
				t.Errorf("temp file left behind: %s", e.Name())
			}
		}
	})
}

// createTarGz builds a tar.gz archive from a map of path→content.
func createTarGz(t *testing.T, files map[string]string) *bytes.Reader {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	for name, content := range files {
		hdr := &tar.Header{
			Name:     name,
			Mode:     0o755,
			Size:     int64(len(content)),
			Typeflag: tar.TypeReg,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}

	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	return bytes.NewReader(buf.Bytes())
}
