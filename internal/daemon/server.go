package daemon

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"

	version "github.com/rbaliyan/go-version"
	kubeportv1 "github.com/rbaliyan/kubeport/api/kubeport/v1"
	"github.com/rbaliyan/kubeport/internal/proxy"
	"github.com/rbaliyan/kubeport/pkg/config"
	"github.com/rbaliyan/kubeport/pkg/grpcauth"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
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
	Mappings(clusterDomain string) []proxy.AddressMapping
}

// Server wraps a gRPC server that exposes the DaemonService over a Unix domain socket or TCP.
type Server struct {
	kubeportv1.UnimplementedDaemonServiceServer

	mgr        Supervisor
	cfg        *config.Config
	grpcServer *grpc.Server
	listenCfg  config.ListenConfig
	apiKey     string
}

// NewServer creates a new daemon gRPC server.
func NewServer(mgr Supervisor, cfg *config.Config) *Server {
	return &Server{
		mgr:       mgr,
		cfg:       cfg,
		listenCfg: cfg.ListenAddress(),
		apiKey:    cfg.APIKey,
	}
}

// Start listens on the configured address and serves gRPC requests.
// This should be called in a goroutine.
func (s *Server) Start() error {
	switch s.listenCfg.Mode {
	case config.ListenTCP:
		return s.startTCP()
	default:
		return s.startUnix()
	}
}

func (s *Server) startUnix() error {
	socketPath := s.listenCfg.Address
	if err := CleanStaleSocket(socketPath); err != nil {
		return err
	}

	lis, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", socketPath, err)
	}

	// Restrict socket to owner only (prevents other users from controlling daemon)
	if err := os.Chmod(socketPath, 0600); err != nil {
		_ = lis.Close()
		return fmt.Errorf("chmod socket %s: %w", socketPath, err)
	}

	s.grpcServer = grpc.NewServer()
	kubeportv1.RegisterDaemonServiceServer(s.grpcServer, s)

	return s.grpcServer.Serve(lis)
}

func (s *Server) startTCP() error {
	lis, err := net.Listen("tcp", s.listenCfg.Address)
	if err != nil {
		return fmt.Errorf("listen on tcp %s: %w", s.listenCfg.Address, err)
	}

	// Determine cert path next to config file (or CWD if no config file).
	certDir := "."
	if s.cfg.FilePath() != "" {
		certDir = filepath.Dir(s.cfg.FilePath())
	}
	tlsCert, err := loadOrGenerateCert(certDir)
	if err != nil {
		_ = lis.Close()
		return fmt.Errorf("TLS certificate: %w", err)
	}

	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{tlsCert},
		MinVersion:   tls.VersionTLS12,
	}
	creds := credentials.NewTLS(tlsCfg)

	s.grpcServer = grpc.NewServer(
		grpc.Creds(creds),
		grpc.UnaryInterceptor(grpcauth.ServerInterceptor(s.apiKey)),
	)
	kubeportv1.RegisterDaemonServiceServer(s.grpcServer, s)

	return s.grpcServer.Serve(lis)
}

// TLSCertFilePath returns the path of the daemon's TLS certificate for a given
// certDir. Callers (CLI, SDK) use this to load the cert for pinning.
func TLSCertFilePath(certDir string) string {
	return filepath.Join(certDir, ".kubeport-tls.crt")
}

// loadOrGenerateCert loads an existing self-signed TLS cert from certDir, or
// generates a new ECDSA P-256 cert valid for 10 years and saves it there.
// The cert and key are stored in .kubeport-tls.crt and .kubeport-tls.key
// respectively, both with mode 0600.
func loadOrGenerateCert(certDir string) (tls.Certificate, error) {
	certFile := TLSCertFilePath(certDir)
	keyFile := filepath.Join(certDir, ".kubeport-tls.key")

	// Try loading existing cert.
	if cert, err := tls.LoadX509KeyPair(certFile, keyFile); err == nil {
		return cert, nil
	}

	// Generate a new self-signed ECDSA P-256 certificate.
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("generate key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("generate serial: %w", err)
	}

	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "kubeport-daemon"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.IPv6loopback},
		DNSNames:     []string{"localhost"},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("create certificate: %w", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("marshal key: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	if err := os.WriteFile(certFile, certPEM, 0600); err != nil {
		return tls.Certificate{}, fmt.Errorf("write cert file: %w", err)
	}
	if err := os.WriteFile(keyFile, keyPEM, 0600); err != nil {
		return tls.Certificate{}, fmt.Errorf("write key file: %w", err)
	}

	return tls.X509KeyPair(certPEM, keyPEM)
}

// Shutdown gracefully stops the gRPC server with a 5-second hard deadline,
// then removes the socket file (Unix mode only).
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

	if s.listenCfg.Mode == config.ListenUnix {
		_ = os.Remove(s.listenCfg.Address)
	}
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
	// Trigger manager shutdown asynchronously so the gRPC response is sent first.
	// mgr.Stop() only stops port-forward supervisors and does not affect the gRPC
	// server's network layer, so the response can complete independently.
	go s.mgr.Stop()

	return &kubeportv1.StopResponse{Success: true}, nil
}

// AddService implements DaemonService.AddService.
func (s *Server) AddService(_ context.Context, req *kubeportv1.AddServiceRequest) (*kubeportv1.AddServiceResponse, error) {
	if req.Service == nil {
		return nil, status.Error(codes.InvalidArgument, "service is required")
	}

	svc := serviceInfoToConfig(req.Service)

	// Apply multi-port config if provided
	if req.Ports != nil {
		ports, excludePorts, offset := portSpecToConfig(req.Ports)
		svc.Ports = ports
		svc.ExcludePorts = excludePorts
		svc.LocalPortOffset = offset
		svc.RemotePort = 0
		svc.LocalPort = 0
	}

	if err := s.mgr.AddService(svc); err != nil {
		return nil, status.Errorf(codes.AlreadyExists, "add service: %v", err)
	}

	if req.Persist && s.cfg.FilePath() != "" {
		cfg, loadErr := config.LoadForEdit(s.cfg.FilePath())
		if loadErr == nil {
			if addErr := cfg.AddService(svc); addErr == nil {
				_ = cfg.Save()
			}
		}
	}

	actualPort := int32(svc.LocalPort)
	// For dynamic ports the OS-assigned port is not known until the port-forward
	// goroutine becomes ready. Wait briefly so the response is useful to callers.
	if svc.LocalPort == 0 && !svc.IsMultiPort() {
		actualPort = s.waitForActualPort(svc.Name, 5*time.Second)
	}

	return &kubeportv1.AddServiceResponse{
		Success:    true,
		ActualPort: actualPort,
	}, nil
}

// waitForActualPort polls Status until the named service reports a non-zero
// ActualPort or the timeout expires. Returns 0 if the port is not yet known.
func (s *Server) waitForActualPort(name string, timeout time.Duration) int32 {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, fs := range s.mgr.Status() {
			if fs.Service.Name == name && fs.ActualPort > 0 {
				return int32(fs.ActualPort)
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return 0
}

// RemoveService implements DaemonService.RemoveService.
func (s *Server) RemoveService(_ context.Context, req *kubeportv1.RemoveServiceRequest) (*kubeportv1.RemoveServiceResponse, error) {
	if req.Name == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}

	if err := s.mgr.RemoveService(req.Name); err != nil {
		return nil, status.Errorf(codes.NotFound, "remove service: %v", err)
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
		return nil, status.Error(codes.FailedPrecondition, "no config file to reload (CLI-only mode)")
	}

	newCfg, err := config.Load(s.cfg.FilePath())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "reload config: %v", err)
	}

	if err := newCfg.Validate(); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "validate config: %v", err)
	}

	added, removed, err := s.mgr.Reload(newCfg)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "reload: %v", err)
	}

	return &kubeportv1.ReloadResponse{
		Success: true,
		Added:   int32(added),
		Removed: int32(removed),
	}, nil
}

// Mappings implements DaemonService.Mappings.
func (s *Server) Mappings(_ context.Context, req *kubeportv1.MappingsRequest) (*kubeportv1.MappingsResponse, error) {
	mappings := s.mgr.Mappings(req.GetClusterDomain())

	addrs := make(map[string]string, len(mappings))
	protos := make([]*kubeportv1.AddressMapping, 0, len(mappings))
	for _, m := range mappings {
		addrs[m.InternalAddr] = m.LocalAddr
		protos = append(protos, &kubeportv1.AddressMapping{
			InternalAddr: m.InternalAddr,
			LocalAddr:    m.LocalAddr,
			ServiceName:  m.ServiceName,
		})
	}

	return &kubeportv1.MappingsResponse{
		Addrs:     addrs,
		Mappings:  protos,
		Context:   s.cfg.Context,
		Namespace: s.cfg.Namespace,
	}, nil
}

// Apply implements DaemonService.Apply.
func (s *Server) Apply(_ context.Context, req *kubeportv1.ApplyRequest) (*kubeportv1.ApplyResponse, error) {
	if len(req.Services) == 0 {
		return nil, status.Error(codes.InvalidArgument, "no services provided")
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
