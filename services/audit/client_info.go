package audit

import (
	"context"
	"net"
	"strings"

	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
)

// Metadata keys the gateway stamps on every request (see
// pkg/server.ClientMetadataAnnotator). They are duplicated here rather
// than imported so the audit module keeps its narrow dependency on
// pkg/server's Components surface only.
const (
	clientIPMetadataKey        = "x-client-ip"
	clientUserAgentMetadataKey = "x-client-user-agent"
)

// enrichClientInfo fills an event's source IP and user agent from the
// request context when the caller did not supply them. Every mutating RPC
// passes its handler context to the recorder, so the gateway-annotated
// values are available without touching each call site.
func enrichClientInfo(ctx context.Context, ev *AuditEvent) {
	if ev == nil {
		return
	}
	if ev.IPAddress == "" {
		ev.IPAddress = clientIPFromContext(ctx)
	}
	if ev.UserAgent == "" {
		ev.UserAgent = clientUserAgentFromContext(ctx)
	}
}

// clientIPFromContext returns the annotated caller IP, falling back to the
// transport peer (a direct gRPC caller has no gateway annotation).
func clientIPFromContext(ctx context.Context) string {
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if values := md.Get(clientIPMetadataKey); len(values) > 0 {
			if ip := strings.TrimSpace(values[0]); ip != "" {
				return ip
			}
		}
	}
	if p, ok := peer.FromContext(ctx); ok && p.Addr != nil {
		addr := p.Addr.String()
		if host, _, err := net.SplitHostPort(addr); err == nil {
			return strings.Trim(host, "[]")
		}
		return strings.Trim(addr, "[]")
	}
	return ""
}

// clientUserAgentFromContext returns the annotated caller user agent.
func clientUserAgentFromContext(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	values := md.Get(clientUserAgentMetadataKey)
	if len(values) == 0 {
		return ""
	}
	return strings.TrimSpace(values[0])
}
