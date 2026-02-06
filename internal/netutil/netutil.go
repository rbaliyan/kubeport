// Package netutil provides shared network utility functions.
package netutil

import (
	"fmt"
	"net"
	"time"
)

// IsPortOpen checks if a TCP port is accepting connections on localhost.
func IsPortOpen(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", port), time.Second)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
