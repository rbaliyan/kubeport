package daemon

import (
	"github.com/rbaliyan/kubeport/pkg/grpcauth"
	"google.golang.org/grpc"
)

// apiKeyInterceptor returns a gRPC unary server interceptor that validates
// the "authorization" metadata header against the expected API key.
func apiKeyInterceptor(key string) grpc.UnaryServerInterceptor {
	return grpcauth.ServerInterceptor(key)
}

// APIKeyClientInterceptor returns a gRPC unary client interceptor that injects
// a "Bearer <key>" authorization header into outgoing requests.
func APIKeyClientInterceptor(key string) grpc.UnaryClientInterceptor {
	return grpcauth.ClientInterceptor(key)
}
