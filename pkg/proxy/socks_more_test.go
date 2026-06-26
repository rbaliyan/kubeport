package proxy

import (
	"io"
	"log/slog"
	"net"
	"testing"
	"time"
)

// feedReadRequest drives SOCKSServer.readRequest by writing req to one end of a
// pipe and draining anything the server writes back (replies for error cases).
// It returns the parsed address and error from readRequest.
func feedReadRequest(t *testing.T, req []byte) (string, error) {
	t.Helper()
	serverConn, clientConn := net.Pipe()
	s := &SOCKSServer{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

	// Drain whatever the server writes (sendReply) so writes never block.
	go func() {
		buf := make([]byte, 64)
		for {
			_ = clientConn.SetReadDeadline(time.Now().Add(time.Second))
			if _, err := clientConn.Read(buf); err != nil {
				return
			}
		}
	}()

	// Feed the request bytes then close so io.ReadFull sees EOF on truncation.
	go func() {
		_, _ = clientConn.Write(req)
		_ = clientConn.Close()
	}()

	addr, err := s.readRequest(serverConn)
	_ = serverConn.Close()
	return addr, err
}

func TestReadRequest(t *testing.T) {
	tests := []struct {
		name    string
		req     []byte
		wantErr bool
		want    string
	}{
		{
			name: "valid IPv4 connect",
			// VER CMD RSV ATYP(IPv4) | 127.0.0.1 | port 80
			req:  []byte{0x05, 0x01, 0x00, addrIPv4, 127, 0, 0, 1, 0x00, 0x50},
			want: "127.0.0.1:80",
		},
		{
			name: "valid domain connect",
			// VER CMD RSV ATYP(domain) | len=3 "abc" | port 443
			req:  append([]byte{0x05, 0x01, 0x00, addrDomain, 0x03}, append([]byte("abc"), 0x01, 0xBB)...),
			want: "abc:443",
		},
		{
			name:    "bad version byte",
			req:     []byte{0x04, 0x01, 0x00, addrIPv4, 127, 0, 0, 1, 0x00, 0x50},
			wantErr: true,
		},
		{
			name:    "unsupported command",
			req:     []byte{0x05, 0x02, 0x00, addrIPv4, 127, 0, 0, 1, 0x00, 0x50},
			wantErr: true,
		},
		{
			name:    "unsupported address type",
			req:     []byte{0x05, 0x01, 0x00, 0xEE},
			wantErr: true,
		},
		{
			name:    "truncated header",
			req:     []byte{0x05, 0x01},
			wantErr: true,
		},
		{
			name:    "truncated ipv4 address",
			req:     []byte{0x05, 0x01, 0x00, addrIPv4, 127, 0},
			wantErr: true,
		},
		{
			name:    "truncated port",
			req:     []byte{0x05, 0x01, 0x00, addrIPv4, 127, 0, 0, 1},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := feedReadRequest(t, tt.req)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got addr %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("addr = %q, want %q", got, tt.want)
			}
		})
	}
}
