package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	runtime "github.com/grpc-ecosystem/grpc-gateway/v2/runtime"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubUploadAPI is a ModelUploadSessionAPI that records which method ran, so
// the route test can tell the list handler apart from the get handler.
type stubUploadAPI struct {
	mu         sync.Mutex
	last       string
	createView *ModelUploadSessionView
}

func (s *stubUploadAPI) CreateUploadSession(_ context.Context, _ ModelUploadCreateRequest) (*ModelUploadSessionView, error) {
	s.mu.Lock()
	s.last = "create"
	s.mu.Unlock()
	return s.createView, nil
}
func (s *stubUploadAPI) GetUploadSession(_ context.Context, _ string) (*ModelUploadSessionView, error) {
	s.mu.Lock()
	s.last = "get"
	s.mu.Unlock()
	return s.createView, nil
}
func (s *stubUploadAPI) ListUploadSessions(_ context.Context, _ int) ([]*ModelUploadSessionView, error) {
	s.mu.Lock()
	s.last = "list"
	s.mu.Unlock()
	return []*ModelUploadSessionView{}, nil
}
func (s *stubUploadAPI) ListUploadedParts(_ context.Context, _ string) ([]int, error) {
	s.mu.Lock()
	s.last = "parts"
	s.mu.Unlock()
	return []int{}, nil
}
func (s *stubUploadAPI) UploadPart(_ context.Context, _ string, _ int, _ io.Reader, _ int64) error {
	s.mu.Lock()
	s.last = "part"
	s.mu.Unlock()
	return nil
}
func (s *stubUploadAPI) CompleteUploadSession(_ context.Context, _ string) (*ModelUploadSessionView, error) {
	s.mu.Lock()
	s.last = "complete"
	s.mu.Unlock()
	return s.createView, nil
}
func (s *stubUploadAPI) CancelUploadSession(_ context.Context, _ string) error {
	s.mu.Lock()
	s.last = "cancel"
	s.mu.Unlock()
	return nil
}

// TestUploadSessionRoutePrecedence proves the literal /uploads collection route
// wins over the /uploads/{id} wildcard for a GET, which the console's drawer
// depends on: /uploads/list must reach ListUploadSessions, never the get-by-id
// handler that would treat "list" as a session id.
func TestUploadSessionRoutePrecedence(t *testing.T) {
	api := &stubUploadAPI{}
	srv, err := NewServer(&Options{Name: "test", ServicePort: 1, MonitorPort: 2, EnableGateway: true, GatewayPort: 3})
	require.NoError(t, err)
	srv.SetModelUploadSession(api)
	cs := srv.(*commonServer)
	cs.gatewayMux = runtime.NewServeMux()
	cs.registerModelUploadSessionRoutes()
	mux := cs.gatewayMux

	mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/admin/models/uploads", nil))
	api.mu.Lock()
	last := api.last
	api.last = ""
	api.mu.Unlock()
	assert.Equal(t, "list", last, "GET /uploads must dispatch to the list handler")

	mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/admin/models/uploads/abc-123", nil))
	api.mu.Lock()
	last = api.last
	api.mu.Unlock()
	assert.Equal(t, "get", last, "GET /uploads/{id} must dispatch to the get handler")
}

// TestUploadSessionPartRoute proves a PUT part routes to the part handler.
func TestUploadSessionPartRoute(t *testing.T) {
	api := &stubUploadAPI{}
	srv, err := NewServer(&Options{Name: "test", ServicePort: 1, MonitorPort: 2, EnableGateway: true, GatewayPort: 3})
	require.NoError(t, err)
	srv.SetModelUploadSession(api)
	cs := srv.(*commonServer)
	cs.gatewayMux = runtime.NewServeMux()
	cs.registerModelUploadSessionRoutes()
	mux := cs.gatewayMux

	mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPut,
		"/api/v1/admin/models/uploads/abc-123/parts/2", strings.NewReader("data")))
	api.mu.Lock()
	last := api.last
	api.mu.Unlock()
	assert.Equal(t, "part", last, "PUT part must dispatch to the part handler")
}
