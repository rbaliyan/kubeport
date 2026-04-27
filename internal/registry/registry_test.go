package registry

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestOpen_RequiresDir(t *testing.T) {
	if _, err := Open(""); err == nil {
		t.Fatal("expected error for empty dir")
	}
}

func TestOpen_CreatesDir(t *testing.T) {
	tmp := t.TempDir()
	dir := filepath.Join(tmp, "nested", "registry")
	r, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if r == nil {
		t.Fatal("expected non-nil registry")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("expected registry dir to exist: %v", err)
	}
}

func TestRegister_RejectsInvalidEntry(t *testing.T) {
	r, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := r.Register(Entry{}); !errors.Is(err, ErrInvalidEntry) {
		t.Fatalf("expected ErrInvalidEntry for zero entry, got %v", err)
	}
	if err := r.Register(Entry{PID: -1, Socket: "/tmp/x.sock"}); !errors.Is(err, ErrInvalidEntry) {
		t.Fatalf("expected ErrInvalidEntry for negative PID, got %v", err)
	}
	if err := r.Register(Entry{PID: 1234}); !errors.Is(err, ErrInvalidEntry) {
		t.Fatalf("expected ErrInvalidEntry without socket/tcp, got %v", err)
	}
}

func TestRegisterAndList_RoundTrip(t *testing.T) {
	r, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	e := Entry{
		PID:        os.Getpid(),
		ConfigFile: "/tmp/kubeport.yaml",
		Socket:     "/tmp/kubeport.sock",
		StartedAt:  time.Now(),
	}
	if err := r.Register(e); err != nil {
		t.Fatalf("Register: %v", err)
	}

	got, err := r.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(got))
	}
	if got[0].PID != e.PID || got[0].Socket != e.Socket {
		t.Fatalf("unexpected entry: %+v", got[0])
	}
}

func TestRegister_UpdatesExisting(t *testing.T) {
	r, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	pid := os.Getpid()
	if err := r.Register(Entry{PID: pid, Socket: "/tmp/a.sock", Version: "v1"}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := r.Register(Entry{PID: pid, Socket: "/tmp/b.sock", Version: "v2"}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	got, err := r.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(got))
	}
	if got[0].Version != "v2" || got[0].Socket != "/tmp/b.sock" {
		t.Fatalf("expected updated entry, got %+v", got[0])
	}
}

func TestList_PrunesDeadEntries(t *testing.T) {
	r, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := r.Register(Entry{PID: os.Getpid(), Socket: "/tmp/a.sock"}); err != nil {
		t.Fatalf("Register self: %v", err)
	}
	// PID 999999999 is virtually guaranteed to be dead.
	if err := r.Register(Entry{PID: 999999999, Socket: "/tmp/b.sock"}); err != nil {
		t.Fatalf("Register dead: %v", err)
	}

	got, err := r.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].PID != os.Getpid() {
		t.Fatalf("expected only live entry, got %+v", got)
	}
}

func TestDeregister(t *testing.T) {
	r, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := r.Register(Entry{PID: os.Getpid(), Socket: "/tmp/a.sock"}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := r.Deregister(os.Getpid()); err != nil {
		t.Fatalf("Deregister: %v", err)
	}
	if err := r.Deregister(os.Getpid()); err != nil {
		t.Fatalf("Deregister missing pid should be no-op: %v", err)
	}

	got, err := r.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty after deregister, got %+v", got)
	}
}

func TestFindByConfig(t *testing.T) {
	dir := t.TempDir()
	r, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	cfgPath := filepath.Join(dir, "kubeport.yaml")
	if err := os.WriteFile(cfgPath, []byte("services: []\n"), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := r.Register(Entry{PID: os.Getpid(), ConfigFile: cfgPath, Socket: "/tmp/a.sock"}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	got, err := r.FindByConfig(cfgPath)
	if err != nil {
		t.Fatalf("FindByConfig: %v", err)
	}
	if got == nil {
		t.Fatal("expected a match")
	}
	if got.PID != os.Getpid() {
		t.Fatalf("unexpected PID: %d", got.PID)
	}

	other := filepath.Join(dir, "other.yaml")
	got, err = r.FindByConfig(other)
	if err != nil {
		t.Fatalf("FindByConfig other: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil for non-matching path, got %+v", got)
	}
}

func TestFindByConfig_Empty(t *testing.T) {
	r, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	got, err := r.FindByConfig("")
	if err != nil {
		t.Fatalf("FindByConfig(\"\"): %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil for empty cfgPath, got %+v", got)
	}
}

func TestList_RejectsCorruptStore(t *testing.T) {
	dir := t.TempDir()
	r, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "instances.json"), []byte("{not json"), 0600); err != nil {
		t.Fatalf("write corrupt: %v", err)
	}
	if _, err := r.List(); err == nil {
		t.Fatal("expected error reading corrupt store")
	}
}

func TestHashKey(t *testing.T) {
	if got := HashKey(""); got != "" {
		t.Fatalf("expected empty hash for empty key, got %q", got)
	}
	a := HashKey("secret")
	b := HashKey("secret")
	if a != b || a == "" {
		t.Fatalf("expected stable non-empty hash, got %q vs %q", a, b)
	}
	if HashKey("secret-2") == a {
		t.Fatal("expected different hash for different key")
	}
}

func TestConcurrentRegister(t *testing.T) {
	r, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	const writers = 8
	var wg sync.WaitGroup
	wg.Add(writers)
	for i := range writers {
		pid := os.Getpid() + i + 1
		// Ensure dead PIDs are pruned by List(); use os.Getpid() as a guaranteed-live anchor.
		go func(pid int) {
			defer wg.Done()
			_ = r.Register(Entry{PID: os.Getpid(), Socket: "/tmp/self.sock"})
			_ = r.Register(Entry{PID: pid, Socket: "/tmp/x.sock"})
			_ = r.Deregister(pid)
		}(pid)
	}
	wg.Wait()

	got, err := r.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, e := range got {
		if e.PID != os.Getpid() {
			t.Fatalf("unexpected entry left after concurrent run: %+v", e)
		}
	}
}
