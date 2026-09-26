package fvt

import (
	"bufio"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeRedisServer is a minimal RESP server implementing the subset of
// commands used by the auth SessionStore: PING, HSET, HGETALL, EXPIRE,
// EXISTS, SET, GET, DEL. It lets the session layer be tested without a
// live Redis.
type fakeRedisServer struct {
	addr string

	mu      sync.Mutex
	entries map[string]string
	ttls    map[string]time.Duration
	hashes  map[string]map[string]string

	ln net.Listener
}

func newFakeRedisServer(t *testing.T) *fakeRedisServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	s := &fakeRedisServer{
		addr:    ln.Addr().String(),
		entries: map[string]string{},
		ttls:    map[string]time.Duration{},
		hashes:  map[string]map[string]string{},
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
			delete(s.hashes, args[1])
			s.mu.Unlock()
			_, _ = w.WriteString(":1\r\n")
		case "EXISTS":
			s.mu.Lock()
			_, ok1 := s.entries[args[1]]
			_, ok2 := s.hashes[args[1]]
			s.mu.Unlock()
			if ok1 || ok2 {
				_, _ = w.WriteString(":1\r\n")
			} else {
				_, _ = w.WriteString(":0\r\n")
			}
		case "EXPIRE":
			s.mu.Lock()
			if _, ok := s.entries[args[1]]; ok {
				secs, _ := strconv.Atoi(args[2])
				s.ttls[args[1]] = time.Duration(secs) * time.Second
			}
			s.mu.Unlock()
			_, _ = w.WriteString(":1\r\n")
		case "HSET":
			s.mu.Lock()
			if s.hashes[args[1]] == nil {
				s.hashes[args[1]] = map[string]string{}
			}
			for i := 2; i+1 < len(args); i += 2 {
				s.hashes[args[1]][args[i]] = args[i+1]
			}
			s.mu.Unlock()
			_, _ = w.WriteString(":1\r\n")
		case "HGETALL":
			s.mu.Lock()
			h := s.hashes[args[1]]
			s.mu.Unlock()
			if len(h) == 0 {
				_, _ = w.WriteString("*0\r\n")
			} else {
				_, _ = fmt.Fprintf(w, "*%d\r\n", len(h)*2)
				for k, v := range h {
					_, _ = fmt.Fprintf(w, "$%d\r\n%s\r\n", len(k), k)
					_, _ = fmt.Fprintf(w, "$%d\r\n%s\r\n", len(v), v)
				}
			}
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
			return nil, fmt.Errorf("bad bulk header")
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
