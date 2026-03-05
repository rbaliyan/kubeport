package proxy_test

import (
	"context"
	"fmt"
	"net/http"

	"github.com/rbaliyan/kubeport/pkg/proxy"
)

func ExampleNew() {
	// Connect to the kubeport daemon with auto-discovery.
	// The proxy searches for the daemon in this order:
	//   1. kubeport.yaml config file (discovers socket path or TCP address)
	//   2. .kubeport.sock next to the config file
	//   3. .kubeport.sock in the current directory
	//   4. ~/.config/kubeport/.kubeport.sock
	//   5. ~/.kubeport/.kubeport.sock
	p, err := proxy.New()
	if err != nil {
		fmt.Println("kubeport not running, using direct connections")
		p = proxy.Noop()
	}
	defer p.Close(context.Background())

	// Use the proxy's dialer with an HTTP client
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: p.DialContext,
		},
	}

	// This request transparently resolves K8s DNS names to localhost ports
	resp, err := client.Get("http://my-api.default.svc.cluster.local:8080/health")
	if err == nil {
		resp.Body.Close()
	}
}

func ExampleNew_withEnabled() {
	// WithEnabled(true) returns a noop proxy on connection errors
	// instead of failing. Ideal for code that should work both locally
	// and in-cluster without changes.
	p, err := proxy.New(proxy.WithEnabled(true))
	if err != nil {
		// This branch is never reached with WithEnabled(true)
		panic("unreachable")
	}
	defer p.Close(context.Background())

	// Works whether kubeport is running or not
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: p.DialContext,
		},
	}
	_ = client
}

func ExampleNew_withSocketPath() {
	// Connect to a specific kubeport daemon socket
	p, err := proxy.New(proxy.WithSocketPath("/path/to/.kubeport.sock"))
	if err != nil {
		fmt.Printf("failed to connect: %v\n", err)
		return
	}
	defer p.Close(context.Background())

	fmt.Printf("connected with %d address mappings\n", len(p.Addrs()))
}

func ExampleNew_withTCP() {
	// Connect to a remote kubeport daemon over TCP
	p, err := proxy.New(proxy.WithTCP("10.0.0.5:9090", "my-api-key"))
	if err != nil {
		fmt.Printf("failed to connect: %v\n", err)
		return
	}
	defer p.Close(context.Background())
	_ = p
}

func ExampleProxy_dialContext() {
	p, _ := proxy.New(proxy.WithEnabled(true))
	defer p.Close(context.Background())

	// DialContext respects the provided context for timeouts and cancellation.
	ctx, cancel := context.WithTimeout(context.Background(), 5e9) // 5s
	defer cancel()

	conn, err := p.DialContext(ctx, "tcp", "redis-0.redis.default.svc.cluster.local:6379")
	if err != nil {
		fmt.Printf("dial error: %v\n", err)
		return
	}
	defer conn.Close()
	fmt.Println("connected to redis")
}

func ExampleNoop() {
	// Noop returns a passthrough proxy that dials addresses directly.
	// All methods work transparently with no translation.
	p := proxy.Noop()
	defer p.Close(context.Background())

	fmt.Println(p.GRPCTarget("localhost:8080")) // prints: localhost:8080
	fmt.Println(p.Addrs())                      // prints: map[]
	fmt.Println(p.Refresh(context.Background()))                     // prints: <nil>

	// Output:
	// localhost:8080
	// map[]
	// <nil>
}
