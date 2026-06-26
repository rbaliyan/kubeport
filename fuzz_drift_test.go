package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// fuzzFuncRe matches a top-level Go fuzz target declaration.
var fuzzFuncRe = regexp.MustCompile(`(?m)^func (Fuzz[A-Za-z0-9_]*)\(`)

// TestFuzzTargetsRegisteredInClusterFuzzLite is a drift guard: every Go fuzz
// target (func Fuzz*) in the repository must be wired into the ClusterFuzzLite
// build script (.clusterfuzzlite/build.sh) via a compile_native_go_fuzzer line.
// If this fails, a fuzz target was added without registering it for CI fuzzing
// — add the corresponding compile_native_go_fuzzer line to build.sh.
func TestFuzzTargetsRegisteredInClusterFuzzLite(t *testing.T) {
	buildScript, err := os.ReadFile(filepath.Join(".clusterfuzzlite", "build.sh"))
	if err != nil {
		t.Fatalf("read build.sh: %v", err)
	}
	script := string(buildScript)

	found := map[string]string{} // fuzz func name -> relative path of first sighting

	walkErr := filepath.WalkDir(".", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// Skip vendored, generated, and VCS directories.
			switch d.Name() {
			case ".git", "vendor", "testdata", "api":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, m := range fuzzFuncRe.FindAllStringSubmatch(string(data), -1) {
			name := m[1]
			if _, ok := found[name]; !ok {
				found[name] = path
			}
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk repo: %v", walkErr)
	}

	if len(found) == 0 {
		t.Fatal("no fuzz targets found; the drift guard is misconfigured")
	}

	for name, path := range found {
		// build.sh registers targets by exact function name as the second
		// argument to compile_native_go_fuzzer.
		if !strings.Contains(script, " "+name+" ") {
			t.Errorf("fuzz target %s (defined in %s) is not registered in .clusterfuzzlite/build.sh; add a compile_native_go_fuzzer line for it", name, path)
		}
	}
}
