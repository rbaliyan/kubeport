package daemon

import (
	"context"
	"fmt"
	"net"
	"os"
	"time"

	version "github.com/rbaliyan/go-version"
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
	AddService(svc config.ServiceConfig) error
	RemoveService(name string) error
	Reload(cfg *config.Config) (added, removed int, err error)
	Apply(services []config.ServiceConfig) (added, skipped int, warnings []string)
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
		Version:   version.Get().Raw,
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

// AddService implements DaemonService.AddService.
func (s *Server) AddService(_ context.Context, req *kubeportv1.AddServiceRequest) (*kubeportv1.AddServiceResponse, error) {
	if req.Service == nil {
		return &kubeportv1.AddServiceResponse{Error: "service is required"}, nil
	}

	svc := serviceInfoToConfig(req.Service)

	if err := s.mgr.AddService(svc); err != nil {
		return &kubeportv1.AddServiceResponse{Error: err.Error()}, nil
	}

	if req.Persist && s.cfg.FilePath() != "" {
		cfg, loadErr := config.LoadForEdit(s.cfg.FilePath())
		if loadErr == nil {
			if addErr := cfg.AddService(svc); addErr == nil {
				_ = cfg.Save()
			}
		}
	}

	return &kubeportv1.AddServiceResponse{
		Success:    true,
		ActualPort: int32(svc.LocalPort),
	}, nil
}

// RemoveService implements DaemonService.RemoveService.
func (s *Server) RemoveService(_ context.Context, req *kubeportv1.RemoveServiceRequest) (*kubeportv1.RemoveServiceResponse, error) {
	if req.Name == "" {
		return &kubeportv1.RemoveServiceResponse{Error: "name is required"}, nil
	}

	if err := s.mgr.RemoveService(req.Name); err != nil {
		return &kubeportv1.RemoveServiceResponse{Error: err.Error()}, nil
	}

	if req.Persist && s.cfg.FilePath() != "" {
		cfg, loadErr := config.LoadForEdit(s.cfg.FilePath())
		if loadErr == nil {
			if rmErr := cfg.RemoveService(req.Name); rmErr == nil {
				_ = cfg.Save()
			}
		}
	}

	return &kubeportv1.RemoveServiceResponse{Success: true}, nil
}

// Reload implements DaemonService.Reload.
func (s *Server) Reload(_ context.Context, _ *kubeportv1.ReloadRequest) (*kubeportv1.ReloadResponse, error) {
	if s.cfg.FilePath() == "" {
		return &kubeportv1.ReloadResponse{Error: "no config file to reload (CLI-only mode)"}, nil
	}

	newCfg, err := config.Load(s.cfg.FilePath())
	if err != nil {
		return &kubeportv1.ReloadResponse{Error: fmt.Sprintf("reload config: %v", err)}, nil
	}

	if err := newCfg.Validate(); err != nil {
		return &kubeportv1.ReloadResponse{Error: fmt.Sprintf("validate config: %v", err)}, nil
	}

	added, removed, err := s.mgr.Reload(newCfg)
	if err != nil {
		return &kubeportv1.ReloadResponse{Error: err.Error()}, nil
	}

	return &kubeportv1.ReloadResponse{
		Success: true,
		Added:   int32(added),
		Removed: int32(removed),
	}, nil
}

// Apply implements DaemonService.Apply.
func (s *Server) Apply(_ context.Context, req *kubeportv1.ApplyRequest) (*kubeportv1.ApplyResponse, error) {
	if len(req.Services) == 0 {
		return &kubeportv1.ApplyResponse{Error: "no services provided"}, nil
	}

	services := make([]config.ServiceConfig, 0, len(req.Services))
	for _, si := range req.Services {
		services = append(services, serviceInfoToConfig(si))
	}

	added, skipped, warnings := s.mgr.Apply(services)

	return &kubeportv1.ApplyResponse{
		Success:  true,
		Added:    int32(added),
		Skipped:  int32(skipped),
		Warnings: warnings,
	}, nil
}
