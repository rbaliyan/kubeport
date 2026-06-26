package grpcauth

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// FuzzGRPCAuthInterceptor fuzzes ServerInterceptor over arbitrary configured
// keys and arbitrary "authorization" metadata values.
//
// Oracle (matching the unit-test contract in interceptor_test.go):
//   - The interceptor never panics on any header/key bytes.
//   - It accepts the request (calls the handler, returns its result, nil error)
//     iff the header is exactly "Bearer " + key for the configured key.
//   - Every rejection returns codes.Unauthenticated and does not call the
//     handler.
//
// withHeader controls whether the authorization metadata key is present at all,
// so the missing-header rejection path is also exercised.
func FuzzGRPCAuthInterceptor(f *testing.F) {
	f.Add("test-key", "Bearer test-key", true)
	f.Add("test-key", "Bearer wrong-key", true)
	f.Add("test-key", "test-key", true)            // missing "Bearer " prefix
	f.Add("test-key", "Bearer test-key extra", true)
	f.Add("test-key", "bearer test-key", true)     // wrong-case scheme
	f.Add("", "Bearer ", true)                     // empty key, canonical header
	f.Add("test-key", "", false)                   // no authorization header at all
	f.Add("k", "Bearer k", true)

	f.Fuzz(func(t *testing.T, key, header string, withHeader bool) {
		interceptor := ServerInterceptor(key)

		handlerCalled := false
		handler := func(_ context.Context, _ any) (any, error) {
			handlerCalled = true
			return "ok", nil
		}

		ctx := context.Background()
		if withHeader {
			ctx = metadata.NewIncomingContext(ctx, metadata.Pairs("authorization", header))
		} else {
			// Provide metadata without the authorization key so the
			// missing-header (not the missing-metadata) branch is reached.
			ctx = metadata.NewIncomingContext(ctx, metadata.Pairs("other", "value"))
		}

		resp, err := interceptor(ctx, nil, &grpc.UnaryServerInfo{}, handler)

		// A request is valid iff the header is present and exactly the canonical
		// "Bearer <key>" for the configured key.
		valid := withHeader && header == "Bearer "+key

		if valid {
			if err != nil {
				t.Fatalf("valid creds rejected: key=%q header=%q err=%v", key, header, err)
			}
			if !handlerCalled {
				t.Fatalf("valid creds did not reach handler: key=%q header=%q", key, header)
			}
			if resp != "ok" {
				t.Fatalf("valid creds returned %v, want handler result", resp)
			}
			return
		}

		if err == nil {
			t.Fatalf("invalid creds accepted: key=%q header=%q withHeader=%v", key, header, withHeader)
		}
		if handlerCalled {
			t.Fatalf("invalid creds reached handler: key=%q header=%q", key, header)
		}
		if s, ok := status.FromError(err); !ok || s.Code() != codes.Unauthenticated {
			t.Fatalf("invalid creds returned %v, want codes.Unauthenticated", err)
		}
	})
}
