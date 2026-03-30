package cli

import (
	"crypto/tls"
	"crypto/x509"
	"os"

	kubeportv1 "github.com/rbaliyan/kubeport/api/kubeport/v1"
	"github.com/rbaliyan/kubeport/pkg/grpcauth"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

type daemonClient struct {
	conn   *grpc.ClientConn
	client kubeportv1.DaemonServiceClient
}

// dialDaemon attempts to connect to the daemon's Unix domain socket.
// Returns (nil, nil) if the socket file doesn't exist (no daemon with gRPC).
// Returns (nil, err) if the socket exists but dial fails.
// Returns (client, nil) on success.
func dialDaemon(socketPath string) (*daemonClient, error) {
	if _, err := os.Stat(socketPath); os.IsNotExist(err) {
		return nil, nil
	}

	conn, err := grpc.NewClient(
		"unix://"+socketPath,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, err
	}

	return &daemonClient{
		conn:   conn,
		client: kubeportv1.NewDaemonServiceClient(conn),
	}, nil
}

// dialDaemonTCP connects to a remote daemon over TCP with API key auth.
// If certFile is provided the server certificate is pinned (self-signed cert
// support without InsecureSkipVerify). Otherwise the system CA pool is used.
func dialDaemonTCP(host, apiKey, certFile string) (*daemonClient, error) {
	tlsCreds := tlsCredsFromCertFile(certFile)
	conn, err := grpc.NewClient(
		host,
		grpc.WithTransportCredentials(tlsCreds),
		grpc.WithUnaryInterceptor(grpcauth.ClientInterceptor(apiKey)),
	)
	if err != nil {
		return nil, err
	}

	return &daemonClient{
		conn:   conn,
		client: kubeportv1.NewDaemonServiceClient(conn),
	}, nil
}

// tlsCredsFromCertFile builds TLS credentials that trust only the given cert
// (cert pinning). Falls back to system CA pool if certFile is empty or unreadable.
func tlsCredsFromCertFile(certFile string) credentials.TransportCredentials {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if certFile != "" {
		if pem, err := os.ReadFile(certFile); err == nil { // #nosec G304 -- cert file path from user config
			pool := x509.NewCertPool()
			if pool.AppendCertsFromPEM(pem) {
				cfg.RootCAs = pool
			}
		}
	}
	return credentials.NewTLS(cfg)
}

func (d *daemonClient) Close() {
	if d.conn != nil {
		_ = d.conn.Close()
	}
}
