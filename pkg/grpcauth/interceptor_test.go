package grpcauth

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestServerInterceptor_ValidKey(t *testing.T) {
	interceptor := ServerInterceptor("test-key")

	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer test-key"))
	handlerCalled := false
	handler := func(ctx context.Context, req any) (any, error) {
		handlerCalled = true
		return "ok", nil
	}

	resp, err := interceptor(ctx, nil, &grpc.UnaryServerInfo{}, handler)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !handlerCalled {
		t.Fatal("handler was not called")
	}
	if resp != "ok" {
		t.Fatalf("expected 'ok', got %v", resp)
	}
}

func TestServerInterceptor_MissingMetadata(t *testing.T) {
	interceptor := ServerInterceptor("test-key")

	_, err := interceptor(context.Background(), nil, &grpc.UnaryServerInfo{}, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.Unauthenticated {
		t.Fatalf("expected Unauthenticated, got %v", err)
	}
}

func TestServerInterceptor_MissingAuthHeader(t *testing.T) {
	interceptor := ServerInterceptor("test-key")

	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("other", "value"))
	_, err := interceptor(ctx, nil, &grpc.UnaryServerInfo{}, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.Unauthenticated {
		t.Fatalf("expected Unauthenticated, got %v", err)
	}
}

func TestServerInterceptor_WrongKey(t *testing.T) {
	interceptor := ServerInterceptor("test-key")

	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer wrong-key"))
	_, err := interceptor(ctx, nil, &grpc.UnaryServerInfo{}, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.Unauthenticated {
		t.Fatalf("expected Unauthenticated, got %v", err)
	}
}

func TestServerInterceptor_MissingBearerPrefix(t *testing.T) {
	interceptor := ServerInterceptor("test-key")

	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "test-key"))
	_, err := interceptor(ctx, nil, &grpc.UnaryServerInfo{}, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.Unauthenticated {
		t.Fatalf("expected Unauthenticated, got %v", err)
	}
}

func TestClientInterceptor(t *testing.T) {
	interceptor := ClientInterceptor("client-key")

	var captured context.Context
	invoker := func(ctx context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
		captured = ctx
		return nil
	}

	err := interceptor(context.Background(), "/svc/Method", nil, nil, nil, invoker)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	md, ok := metadata.FromOutgoingContext(captured)
	if !ok {
		t.Fatal("expected outgoing metadata on the invoked context")
	}
	vals := md.Get("authorization")
	if len(vals) != 1 {
		t.Fatalf("authorization header count = %d, want 1", len(vals))
	}
	if vals[0] != "Bearer client-key" {
		t.Fatalf("authorization = %q, want 'Bearer client-key'", vals[0])
	}
}
