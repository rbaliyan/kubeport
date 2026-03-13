// Package grpcauth provides gRPC unary interceptors for API key authentication.
// Streaming interceptors are not provided because kubeport uses unary RPCs only.
package grpcauth

import (
	"context"
	"crypto/subtle"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// ServerInterceptor returns a gRPC unary server interceptor that validates
// the "authorization" metadata header against the expected API key.
// The expected format is "Bearer <key>".
func ServerInterceptor(key string) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		_ *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		md, ok := metadata.FromIncomingContext(ctx)
		if !ok {
			return nil, status.Error(codes.Unauthenticated, "missing metadata")
		}

		vals := md.Get("authorization")
		if len(vals) == 0 {
			return nil, status.Error(codes.Unauthenticated, "missing authorization header")
		}

		token, found := strings.CutPrefix(vals[0], "Bearer ")
		if !found || subtle.ConstantTimeCompare([]byte(token), []byte(key)) != 1 {
			return nil, status.Error(codes.Unauthenticated, "invalid API key")
		}

		return handler(ctx, req)
	}
}

// ClientInterceptor returns a gRPC unary client interceptor that injects
// a "Bearer <key>" authorization header into outgoing requests.
func ClientInterceptor(key string) grpc.UnaryClientInterceptor {
	return func(
		ctx context.Context,
		method string,
		req, reply any,
		cc *grpc.ClientConn,
		invoker grpc.UnaryInvoker,
		opts ...grpc.CallOption,
	) error {
		ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+key)
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}
