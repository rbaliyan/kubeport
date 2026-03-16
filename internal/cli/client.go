package cli

import (
	"crypto/tls"
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
// TLS is used with InsecureSkipVerify because the daemon uses a self-signed
// certificate; the API key provides application-level authentication.
func dialDaemonTCP(host, apiKey string) (*daemonClient, error) {
	tlsCreds := credentials.NewTLS(&tls.Config{
		InsecureSkipVerify: true, //nolint:gosec // self-signed cert; auth via API key
		MinVersion:         tls.VersionTLS12,
	})
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

func (d *daemonClient) Close() {
	if d.conn != nil {
		_ = d.conn.Close()
	}
}
