package proxy

import (
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/resolver"
)

const resolverScheme = "kubeport"

type resolverBuilder struct {
	client *client
}

func (b *resolverBuilder) Build(target resolver.Target, cc resolver.ClientConn, _ resolver.BuildOptions) (resolver.Resolver, error) {
	addr := target.Endpoint()
	if addr == "" {
		addr = strings.TrimPrefix(target.URL.Path, "/")
	}

	translated := b.client.translateAddr(addr)

	r := &proxyResolver{cc: cc, addr: translated}
	r.updateState()
	return r, nil
}

func (b *resolverBuilder) Scheme() string {
	return resolverScheme
}

type proxyResolver struct {
	cc   resolver.ClientConn
	addr string
}

func (r *proxyResolver) updateState() {
	_ = r.cc.UpdateState(resolver.State{
		Addresses: []resolver.Address{{Addr: r.addr}},
	})
}

func (r *proxyResolver) ResolveNow(_ resolver.ResolveNowOptions) {}
func (r *proxyResolver) Close()                                  {}

func (c *client) resolverDialOption() grpc.DialOption {
	return grpc.WithResolvers(&resolverBuilder{client: c})
}
