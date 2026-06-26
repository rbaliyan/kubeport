package cli_test

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// buildBinary compiles the kubeport binary into a temp dir and returns its path.
// It skips the test if the Go toolchain is unavailable.
func buildBinary(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}

	bin := filepath.Join(t.TempDir(), "kubeport")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	// The repo root is two levels up from internal/cli.
	cmd := exec.CommandContext(ctx, "go", "build", "-o", bin, "../..")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build kubeport: %v\n%s", err, out)
	}
	return bin
}

// run executes the binary with args under a bounded context and returns its
// combined output and exit code.
func run(t *testing.T, bin string, args ...string) (output string, exitCode int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, args...)
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("command %v timed out", args)
	}
	code := 0
	if exitErr, ok := err.(*exec.ExitError); ok {
		code = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("command %v failed to run: %v", args, err)
	}
	return string(out), code
}

// TestSmoke_BinaryBootsAndVersion exercises the real os.Exit dispatch paths from
// a user's perspective: the binary builds, reports its version, prints help, and
// rejects bad input with a non-zero exit.
func TestSmoke_BinaryBootsAndVersion(t *testing.T) {
	bin := buildBinary(t)

	t.Run("version", func(t *testing.T) {
		out, code := run(t, bin, "version")
		if code != 0 {
			t.Fatalf("version exit = %d, want 0 (output: %s)", code, out)
		}
		if !strings.Contains(out, "kubeport") {
			t.Fatalf("version output %q does not mention kubeport", out)
		}
	})

	t.Run("help", func(t *testing.T) {
		out, code := run(t, bin, "help")
		if code != 0 {
			t.Fatalf("help exit = %d, want 0 (output: %s)", code, out)
		}
		if strings.TrimSpace(out) == "" {
			t.Fatal("help produced no output")
		}
	})

	t.Run("unknown command", func(t *testing.T) {
		_, code := run(t, bin, "definitely-not-a-command")
		if code == 0 {
			t.Fatal("unknown command exit = 0, want non-zero")
		}
	})

	t.Run("bad flag value", func(t *testing.T) {
		// --timeout requires a parseable duration; a garbage value must fail.
		_, code := run(t, bin, "--timeout", "not-a-duration", "status")
		if code == 0 {
			t.Fatal("bad flag value exit = 0, want non-zero")
		}
	})
}
