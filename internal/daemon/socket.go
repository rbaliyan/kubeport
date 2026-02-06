package daemon

import (
	"fmt"
	"net"
	"os"
	"time"
)

// CleanStaleSocket removes a stale Unix domain socket file if no daemon is listening on it.
// Returns nil if the socket was cleaned or didn't exist.
// Returns an error if another daemon is already running.
func CleanStaleSocket(path string) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	}

	// Try to connect — if successful, another daemon is running.
	conn, err := net.DialTimeout("unix", path, 2*time.Second)
	if err == nil {
		conn.Close()
		return fmt.Errorf("another daemon is already running (socket %s is active)", path)
	}

	// Socket exists but no one is listening — stale file, remove it.
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove stale socket %s: %w", path, err)
	}

	return nil
}
