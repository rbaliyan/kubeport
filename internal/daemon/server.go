package daemon

import (
	"context"
	"fmt"
	"net"
	"os"
	"time"

	kubeportv1 "github.com/rbaliyan/kubeport/api/kubeport/v1"
	"github.com/rbaliyan/kubeport/internal/config"
	"github.com/rbaliyan/kubeport/internal/proxy"
	"google.golang.org/grpc"
)

var _ Supervisor = (*proxy.Manager)(nil)

// Supervisor is the interface the daemon server uses to query and control the manager.
// It is satisfied by *proxy.Manager.
type Supervisor interface {
	Status() []proxy.ForwardStatus
	Stop()
}

// Server wraps a gRPC server that exposes the DaemonService over a Unix domain socket.
type Server struct {
	kubeportv1.UnimplementedDaemonServiceServer

	mgr        Supervisor
	cfg        *config.Config
	grpcServer *grpc.Server
	socketPath string
}

// NewServer creates a new daemon gRPC server.
func NewServer(mgr Supervisor, cfg *config.Config) *Server {
	return &Server{
		mgr:        mgr,
		cfg:        cfg,
		socketPath: cfg.SocketFile(),
	}
}

// Start listens on the Unix domain socket and serves gRPC requests.
// This should be called in a goroutine.
func (s *Server) Start() error {
	if err := CleanStaleSocket(s.socketPath); err != nil {
		return err
	}

	lis, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", s.socketPath, err)
	}

	// Restrict socket to owner only (prevents other users from controlling daemon)
	if err := os.Chmod(s.socketPath, 0600); err != nil {
		_ = lis.Close()
		return fmt.Errorf("chmod socket %s: %w", s.socketPath, err)
	}

	s.grpcServer = grpc.NewServer()
	kubeportv1.RegisterDaemonServiceServer(s.grpcServer, s)

	return s.grpcServer.Serve(lis)
}

// Shutdown gracefully stops the gRPC server with a 5-second hard deadline,
// then removes the socket file.
func (s *Server) Shutdown() {
	if s.grpcServer == nil {
		return
	}

	done := make(chan struct{})
	go func() {
		s.grpcServer.GracefulStop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		s.grpcServer.Stop()
	}

	_ = os.Remove(s.socketPath)
}

// Status implements DaemonService.Status.
func (s *Server) Status(_ context.Context, _ *kubeportv1.StatusRequest) (*kubeportv1.StatusResponse, error) {
	statuses := s.mgr.Status()
	forwards := make([]*kubeportv1.ForwardStatusProto, 0, len(statuses))
	for _, fs := range statuses {
		forwards = append(forwards, convertForwardStatus(fs))
	}

	return &kubeportv1.StatusResponse{
		Context:   s.cfg.Context,
		Namespace: s.cfg.Namespace,
		Forwards:  forwards,
	}, nil
}

// Stop implements DaemonService.Stop.
func (s *Server) Stop(_ context.Context, _ *kubeportv1.StopRequest) (*kubeportv1.StopResponse, error) {
	// Trigger manager shutdown in a goroutine so the response can be sent first.
	go func() {
		time.Sleep(100 * time.Millisecond)
		s.mgr.Stop()
	}()

	return &kubeportv1.StopResponse{Success: true}, nil
}
