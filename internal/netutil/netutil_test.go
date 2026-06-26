package netutil

import (
	"net"
	"testing"
)

func TestIsPortOpen(t *testing.T) {
	// Bind an ephemeral port so we have a known-open and a known-closed case.
	lis, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := lis.Addr().(*net.TCPAddr).Port

	t.Run("open port accepts connection", func(t *testing.T) {
		if !IsPortOpen(port) {
			t.Errorf("IsPortOpen(%d) = false, want true while listener is open", port)
		}
	})

	// Close the listener and confirm the same port now reports closed.
	if err := lis.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}

	t.Run("closed port refuses connection", func(t *testing.T) {
		if IsPortOpen(port) {
			t.Errorf("IsPortOpen(%d) = true, want false after listener closed", port)
		}
	})
}
