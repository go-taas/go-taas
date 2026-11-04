package server

// Client IP and user-agent propagation.
//
// The console is served by a separate nginx pod, so the gateway sees the
// proxy's address as the transport peer and the real caller only in the
// forwarding headers. The gateway therefore parks the original client IP
// on the gRPC metadata as x-client-ip (plus x-client-user-agent), which
// services such as audit stamp onto their records. Without it every audit
// row's ip_address is empty.
//
// Only the forwarding headers are trusted here, and only the left-most
// X-Forwarded-For entry: the deployment terminates at a trusted reverse
// proxy. A caller cannot inject x-client-ip directly because the gateway
// header matcher does not forward that key from an incoming request.

import (
	"context"
	"net"
	"net/http"
	"strings"

	"google.golang.org/grpc/metadata"
)

// Metadata keys used by the client-metadata annotator.
const (
	ClientIPMetadataKey        = "x-client-ip"
	ClientUserAgentMetadataKey = "x-client-user-agent"
)

// ClientMetadataAnnotator forwards the caller's IP and user agent into the
// gRPC metadata for every gateway request.
func ClientMetadataAnnotator(_ context.Context, r *http.Request) metadata.MD {
	pairs := make([]string, 0, 4)
	if ip := ClientIPFromRequest(r); ip != "" {
		pairs = append(pairs, ClientIPMetadataKey, ip)
	}
	if ua := strings.TrimSpace(r.UserAgent()); ua != "" {
		pairs = append(pairs, ClientUserAgentMetadataKey, ua)
	}
	return metadata.Pairs(pairs...)
}

// ClientIPFromRequest resolves the caller's IP: the left-most
// X-Forwarded-For entry, then X-Real-IP, then the transport peer.
func ClientIPFromRequest(r *http.Request) string {
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		first, _, _ := strings.Cut(forwarded, ",")
		if ip := normalizeClientIP(first); ip != "" {
			return ip
		}
	}
	if realIP := r.Header.Get("X-Real-IP"); realIP != "" {
		if ip := normalizeClientIP(realIP); ip != "" {
			return ip
		}
	}
	return normalizeClientIP(r.RemoteAddr)
}

// normalizeClientIP trims a port and surrounding brackets, returning ""
// when the value is not an IP address.
func normalizeClientIP(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(raw); err == nil {
		raw = host
	}
	return strings.Trim(raw, "[]")
}
