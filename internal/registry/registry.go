// Package registry maintains a central record of running kubeport instances.
package registry

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/rbaliyan/kubeport/pkg/config"
)

// Entry describes a single running kubeport daemon.
type Entry struct {
	PID        int       `json:"pid"`
	ConfigFile string    `json:"config_file,omitempty"`
	PIDFile    string    `json:"pid_file,omitempty"`
	LogFile    string    `json:"log_file,omitempty"`
	Socket     string    `json:"socket,omitempty"`  // Unix socket path (empty when TCP)
	TCPAddress string    `json:"tcp_address,omitempty"` // host:port (empty when Unix)
	HasAPIKey  bool      `json:"has_api_key"`
	APIKeyHash string    `json:"api_key_hash,omitempty"` // SHA-256 hex of key; never the raw key
	Version    string    `json:"version,omitempty"`
	StartedAt  time.Time `json:"started_at"`
}

// IsAlive reports whether the process is still running.
func (e Entry) IsAlive() bool {
	return syscall.Kill(e.PID, 0) == nil
}

// CanConnect reports whether a caller holding apiKey can authenticate to this instance.
// Both sides having no key (open mode) also returns true.
func (e Entry) CanConnect(apiKey string) bool {
	if !e.HasAPIKey {
		return true // Unix socket, no auth required
	}
	return apiKey != "" && HashKey(apiKey) == e.APIKeyHash
}

// HashKey returns the SHA-256 hex hash of a raw API key.
// An empty key returns an empty string.
func HashKey(key string) string {
	if key == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

type store struct {
	Entries []Entry `json:"entries"`
}

// Registry manages the persistent list of running kubeport instances.
type Registry struct {
	path     string // instances.json
	lockPath string // instances.lock
}

// Open opens (or creates) the instance registry stored in the central directory
// for the given config file path. Pass an empty string to use the default central dir.
func Open(cfgPath string) (*Registry, error) {
	dir := config.CentralDir(cfgPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	return &Registry{
		path:     filepath.Join(dir, "instances.json"),
		lockPath: filepath.Join(dir, "instances.lock"),
	}, nil
}

// Register adds or updates the entry for this process.
func (r *Registry) Register(e Entry) error {
	return r.withLock(func(s *store) {
		for i, ex := range s.Entries {
			if ex.PID == e.PID {
				s.Entries[i] = e
				return
			}
		}
		s.Entries = append(s.Entries, e)
	})
}

// Deregister removes the entry for the given PID.
func (r *Registry) Deregister(pid int) error {
	return r.withLock(func(s *store) {
		out := s.Entries[:0]
		for _, e := range s.Entries {
			if e.PID != pid {
				out = append(out, e)
			}
		}
		s.Entries = out
	})
}

// List returns all alive entries, pruning stale ones from the file.
func (r *Registry) List() ([]Entry, error) {
	var result []Entry
	err := r.withLock(func(s *store) {
		alive := s.Entries[:0]
		for _, e := range s.Entries {
			if e.IsAlive() {
				alive = append(alive, e)
				result = append(result, e)
			}
		}
		s.Entries = alive
	})
	return result, err
}

// FindByConfig returns the first alive entry whose ConfigFile resolves to the
// same absolute path as cfgPath. Returns (nil, nil) when no match is found.
func (r *Registry) FindByConfig(cfgPath string) (*Entry, error) {
	if cfgPath == "" {
		return nil, nil
	}
	abs, err := filepath.Abs(cfgPath)
	if err != nil {
		return nil, err
	}
	entries, err := r.List()
	if err != nil {
		return nil, err
	}
	for i := range entries {
		ea, _ := filepath.Abs(entries[i].ConfigFile)
		if ea == abs {
			return &entries[i], nil
		}
	}
	return nil, nil
}

// withLock acquires an exclusive file lock, reads the store, calls fn to mutate it,
// then writes the result back atomically.
func (r *Registry) withLock(fn func(*store)) error {
	lf, err := os.OpenFile(r.lockPath, os.O_CREATE|os.O_RDWR, 0600) // #nosec G304 -- path is registry-dir controlled
	if err != nil {
		return err
	}
	defer func() { _ = lf.Close() }()

	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer func() { _ = syscall.Flock(int(lf.Fd()), syscall.LOCK_UN) }()

	s := &store{}
	if data, err := os.ReadFile(r.path); err == nil { // #nosec G304
		_ = json.Unmarshal(data, s)
	}

	fn(s)

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil { // #nosec G306
		return err
	}
	return os.Rename(tmp, r.path)
}
