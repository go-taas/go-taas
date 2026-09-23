package auth

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"github.com/go-taas/go-taas/pkg/config"
	authv1 "github.com/go-taas/go-taas/proto/taas/auth/v1"
)

// fakeRedisServer is a minimal RESP server implementing the subset of
// commands used by redisVerdictCache: GET, SET (with EX), DEL and PING.
// It lets the cache layer be tested without a live Redis or a new test
// dependency.
type fakeRedisServer struct {
	t    *testing.T
	addr string

	mu      sync.Mutex
	entries map[string]string
	ttls    map[string]time.Duration

	ln net.Listener
}

func newFakeRedisServer(t *testing.T) *fakeRedisServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	s := &fakeRedisServer{
		t:       t,
		addr:    ln.Addr().String(),
		entries: map[string]string{},
		ttls:    map[string]time.Duration{},
		ln:      ln,
	}
	go s.serve()
	t.Cleanup(func() { _ = ln.Close() })
	return s
}

func (s *fakeRedisServer) serve() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handle(conn)
	}
}

func (s *fakeRedisServer) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	r := bufio.NewReader(conn)
	w := bufio.NewWriter(conn)
	for {
		args, err := readRESPCommand(r)
		if err != nil {
			return
		}
		if len(args) == 0 {
			continue
		}
		cmd := strings.ToUpper(args[0])
		switch cmd {
		case "PING":
			_, _ = w.WriteString("+PONG\r\n")
		case "GET":
			s.mu.Lock()
			v, ok := s.entries[args[1]]
			s.mu.Unlock()
			if !ok {
				_, _ = w.WriteString("$-1\r\n")
			} else {
				_, _ = fmt.Fprintf(w, "$%d\r\n%s\r\n", len(v), v)
			}
		case "SET":
			var ttl time.Duration
			if len(args) >= 5 && strings.ToUpper(args[3]) == "EX" {
				secs, _ := strconv.Atoi(args[4])
				ttl = time.Duration(secs) * time.Second
			}
			s.mu.Lock()
			s.entries[args[1]] = args[2]
			s.ttls[args[1]] = ttl
			s.mu.Unlock()
			_, _ = w.WriteString("+OK\r\n")
		case "DEL":
			s.mu.Lock()
			delete(s.entries, args[1])
			delete(s.ttls, args[1])
			s.mu.Unlock()
			_, _ = w.WriteString(":1\r\n")
		default:
			_, _ = w.WriteString("-ERR unknown command\r\n")
		}
		if err := w.Flush(); err != nil {
			return
		}
	}
}

// readRESPCommand parses one RESP array command from r.
func readRESPCommand(r *bufio.Reader) ([]string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return nil, nil
	}
	if line[0] != '*' {
		return strings.Fields(line), nil
	}
	n, err := strconv.Atoi(line[1:])
	if err != nil {
		return nil, err
	}
	args := make([]string, 0, n)
	for i := 0; i < n; i++ {
		bulk, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		bulk = strings.TrimRight(bulk, "\r\n")
		if len(bulk) == 0 || bulk[0] != '$' {
			return nil, errors.New("bad bulk header")
		}
		size, err := strconv.Atoi(bulk[1:])
		if err != nil {
			return nil, err
		}
		if size < 0 {
			args = append(args, "")
			continue
		}
		buf := make([]byte, size+2) // payload + CRLF
		if _, err := readFull(r, buf); err != nil {
			return nil, err
		}
		args = append(args, string(buf[:size]))
	}
	return args, nil
}

func readFull(r *bufio.Reader, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := r.Read(buf[total:])
		if err != nil {
			return total, err
		}
		total += n
	}
	return total, nil
}

func newTestRedisCache(t *testing.T) (*redisVerdictCache, *fakeRedisServer) {
	t.Helper()
	srv := newFakeRedisServer(t)
	client := redis.NewClient(&redis.Options{Addr: srv.addr})
	t.Cleanup(func() { _ = client.Close() })
	return newRedisVerdictCache(client, 30*time.Second), srv
}

func TestRedisVerdictCacheRoundTrip(t *testing.T) {
	cache, _ := newTestRedisCache(t)
	ctx := context.Background()
	digest := KeyDigest("sk-cache-test")

	// Miss returns nil, nil.
	v, err := cache.Get(ctx, digest)
	require.NoError(t, err)
	assert.Nil(t, v)

	// Set then hit.
	want := &keyVerdict{OrganizationID: "org-a", KeyID: "k1", Role: "agent"}
	require.NoError(t, cache.Set(ctx, digest, want, 30*time.Second))
	got, err := cache.Get(ctx, digest)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, want, got)

	// Delete then miss again.
	require.NoError(t, cache.Delete(ctx, digest))
	v, err = cache.Get(ctx, digest)
	require.NoError(t, err)
	assert.Nil(t, v)
}

func TestRedisVerdictCacheCorruptEntry(t *testing.T) {
	cache, srv := newTestRedisCache(t)
	ctx := context.Background()
	digest := KeyDigest("sk-corrupt")

	srv.mu.Lock()
	srv.entries[cacheKey(digest)] = "{not json"
	srv.mu.Unlock()

	_, err := cache.Get(ctx, digest)
	assert.Error(t, err, "corrupt cache payload must surface an error")
}

func TestServiceLazyWiringFromComponents(t *testing.T) {
	// New(components) must lazily wire the repository and cache on first
	// use; with no components configured the RPCs fail closed with an
	// internal error instead of panicking.
	svc := New(nil)
	_, err := svc.ListAPIKeys(orgCtx("org-a"), &authv1.ListAPIKeysRequest{})
	assert.Error(t, err)

	_, err = svc.VerifyAPIKey(context.Background(), &authv1.VerifyAPIKeyRequest{KeyDigest: KeyDigest("sk-x")})
	assert.Error(t, err)
}

func TestServiceAssemblyMethods(t *testing.T) {
	svc := New(nil)
	assert.Equal(t, ServiceName, svc.ServiceName())
	assert.NotNil(t, svc.GetServiceHandlerRegisterFn())
	assert.NotPanics(t, func() {
		grpcServer := grpc.NewServer()
		svc.AttachToServer(grpcServer)
	})
}

func TestServiceUnimplementedRPCs(t *testing.T) {
	svc := NewWithRepositoryAndCache(nil, nil, config.Argon2Params{})
	_, err := svc.CreateUser(context.Background(), nil)
	assert.Error(t, err)
	_, err = svc.Login(context.Background(), nil)
	assert.Error(t, err)
}
