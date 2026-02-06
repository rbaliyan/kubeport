package cli

import (
	"context"
	"os"
	"time"

	kubeportv1 "github.com/rbaliyan/kubeport/api/kubeport/v1"
	"google.golang.org/grpc"
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

	// Verify connectivity with a short deadline
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	client := kubeportv1.NewDaemonServiceClient(conn)
	if _, err := client.Status(ctx, &kubeportv1.StatusRequest{}); err != nil {
		_ = conn.Close()
		return nil, err
	}

	return &daemonClient{
		conn:   conn,
		client: client,
	}, nil
}

func (d *daemonClient) Close() {
	if d.conn != nil {
		_ = d.conn.Close()
	}
}
