// Package registry maintains a central record of running kubeport instances.
//
// The registry lives in a single directory and persists entries to instances.json.
// All mutations are guarded by an exclusive flock on instances.lock so concurrent
// kubeport processes can safely register, deregister, and list instances.
package registry

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// ErrInvalidEntry is returned when Register is called with an Entry that is
// missing required fields (PID, listen address).
var ErrInvalidEntry = errors.New("invalid registry entry")

// Entry describes a single running kubeport daemon. Entries are pure value data;
// behaviour (liveness checks, lookup) lives on the Registry.
type Entry struct {
	PID        int       `json:"pid"`
	ConfigFile string    `json:"config_file,omitempty"`
	PIDFile    string    `json:"pid_file,omitempty"`
	LogFile    string    `json:"log_file,omitempty"`
	Socket     string    `json:"socket,omitempty"`
	TCPAddress string    `json:"tcp_address,omitempty"`
	AuthEnabled bool   `json:"auth_enabled"`
	KeyID       string `json:"key_id,omitempty"`
	Version    string    `json:"version,omitempty"`
	StartedAt  time.Time `json:"started_at"`
}

// store is the JSON-serialised shape written to instances.json.
type store struct {
	Entries []Entry `json:"entries"`
}

// Registry manages the persistent list of running kubeport instances.
type Registry struct {
	path     string
	lockPath string
}

// Open opens (or creates) an instance registry stored under dir. The directory
// is created with 0700 permissions if it does not exist. Callers typically pass
// the path returned by config.CentralDir.
func Open(dir string) (*Registry, error) {
	if dir == "" {
		return nil, errors.New("registry: dir is required")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("registry: create dir: %w", err)
	}
	return &Registry{
		path:     filepath.Join(dir, "instances.json"),
		lockPath: filepath.Join(dir, "instances.lock"),
	}, nil
}

// Register adds or updates the entry for e.PID. Returns ErrInvalidEntry if the
// entry is missing required fields.
func (r *Registry) Register(e Entry) error {
	if e.PID <= 0 {
		return fmt.Errorf("%w: pid must be positive", ErrInvalidEntry)
	}
	if e.Socket == "" && e.TCPAddress == "" {
		return fmt.Errorf("%w: socket or tcp_address is required", ErrInvalidEntry)
	}
	return r.withLock(func(s *store) error {
		for i, ex := range s.Entries {
			if ex.PID == e.PID {
				s.Entries[i] = e
				return nil
			}
		}
		s.Entries = append(s.Entries, e)
		return nil
	})
}

// Deregister removes the entry for the given PID. Returns nil when no such entry
// exists (deregister is invoked best-effort during shutdown). Lock and I/O
// failures are still propagated as errors so callers can log unexpected problems.
func (r *Registry) Deregister(pid int) error {
	return r.withLock(func(s *store) error {
		out := s.Entries[:0]
		for _, e := range s.Entries {
			if e.PID != pid {
				out = append(out, e)
			}
		}
		s.Entries = out
		return nil
	})
}

// List returns all alive entries, pruning stale ones from the file as a side
// effect. The returned slice is independent of the on-disk store.
func (r *Registry) List() ([]Entry, error) {
	var result []Entry
	err := r.withLock(func(s *store) error {
		alive := s.Entries[:0]
		for _, e := range s.Entries {
			if isAlive(e.PID) {
				alive = append(alive, e)
				result = append(result, e)
			}
		}
		s.Entries = alive
		return nil
	})
	return result, err
}

// FindByConfig returns the first alive entry whose ConfigFile resolves to the
// same absolute path as cfgPath. Returns (nil, nil) when cfgPath is empty or no
// match is found.
func (r *Registry) FindByConfig(cfgPath string) (*Entry, error) {
	if cfgPath == "" {
		return nil, nil
	}
	abs, err := filepath.Abs(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("registry: resolve config path: %w", err)
	}
	entries, err := r.List()
	if err != nil {
		return nil, err
	}
	for i := range entries {
		ea, err := filepath.Abs(entries[i].ConfigFile)
		if err != nil {
			continue
		}
		if ea == abs {
			return &entries[i], nil
		}
	}
	return nil, nil
}

func isAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}

// withLock acquires an exclusive file lock, reads the store, calls fn, then
// writes the result back atomically via rename. Corrupt JSON is reported as an
// error rather than silently truncating the file.
func (r *Registry) withLock(fn func(*store) error) (retErr error) {
	lf, err := os.OpenFile(r.lockPath, os.O_CREATE|os.O_RDWR, 0600) // #nosec G304 -- registry-controlled path
	if err != nil {
		return fmt.Errorf("registry: open lock: %w", err)
	}
	defer func() {
		if cerr := lf.Close(); cerr != nil && retErr == nil {
			retErr = fmt.Errorf("registry: close lock: %w", cerr)
		}
	}()

	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX); err != nil { // #nosec G115 -- fd fits int on all supported platforms
		return fmt.Errorf("registry: acquire lock: %w", err)
	}
	defer func() { _ = syscall.Flock(int(lf.Fd()), syscall.LOCK_UN) }() // #nosec G115

	s := &store{}
	data, err := os.ReadFile(r.path) // #nosec G304 -- registry-controlled path
	switch {
	case err == nil:
		if len(data) > 0 {
			if uerr := json.Unmarshal(data, s); uerr != nil {
				return fmt.Errorf("registry: parse %s: %w", r.path, uerr)
			}
		}
	case errors.Is(err, os.ErrNotExist):
		// fresh registry — start with an empty store
	default:
		return fmt.Errorf("registry: read %s: %w", r.path, err)
	}

	if err := fn(s); err != nil {
		return err
	}

	out, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("registry: marshal: %w", err)
	}
	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, out, 0600); err != nil { // #nosec G306
		return fmt.Errorf("registry: write tmp: %w", err)
	}
	if err := os.Rename(tmp, r.path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("registry: replace %s: %w", r.path, err)
	}
	return nil
}
