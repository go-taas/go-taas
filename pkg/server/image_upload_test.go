package server

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"google.golang.org/grpc/metadata"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// uploadRequest builds a multipart upload around body.
func uploadRequest(t *testing.T, body []byte, fields map[string]string) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	for name, value := range fields {
		require.NoError(t, writer.WriteField(name, value))
	}
	part, err := writer.CreateFormFile("file", "image.tar")
	require.NoError(t, err)
	_, err = part.Write(body)
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	req := httptest.NewRequest(http.MethodPost, ImageUploadPath, &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer session-token")
	return req
}

// TestImageUploadHandlerStagesAndDelegates verifies the handler streams the
// archive to a temp file, forwards the credentials into gRPC metadata, passes
// the form fields through and removes the staged file afterwards.
func TestImageUploadHandlerStagesAndDelegates(t *testing.T) {
	s := &commonServer{}
	var staged string
	var got ImageUploadRequest
	var authHeader string
	s.SetImageUpload(func(ctx context.Context, req ImageUploadRequest) (*ImageUploadResult, error) {
		got = req
		staged = req.ArchivePath
		// The file exists while the push runs.
		_, err := os.Stat(req.ArchivePath)
		require.NoError(t, err)
		authHeader = metadataAuthorization(ctx)
		return &ImageUploadResult{
			ImageID: "img-1", Reference: "hub/taas/x:1", Name: "hub/taas/x", Tag: "1",
		}, nil
	})

	rec := httptest.NewRecorder()
	s.imageUploadHandler(rec, uploadRequest(t, []byte("tar-bytes"), map[string]string{
		"accelerator": "nvidia",
		"engine":      "vllm",
		"description": "uploaded",
		"name":        "x",
		"tag":         "1",
	}))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "nvidia", got.Accelerator)
	assert.Equal(t, "vllm", got.Engine)
	assert.Equal(t, "uploaded", got.Description)
	assert.Equal(t, "x", got.Name)
	assert.Equal(t, "1", got.Tag)
	assert.Equal(t, "Bearer session-token", authHeader,
		"the session token is forwarded to the module that gates the call")

	_, err := os.Stat(staged)
	assert.True(t, os.IsNotExist(err), "the staged archive is removed after the push")
}

// TestImageUploadHandlerRequiresFile verifies an upload without a file part
// is refused.
func TestImageUploadHandlerRequiresFile(t *testing.T) {
	s := &commonServer{}
	s.SetImageUpload(func(context.Context, ImageUploadRequest) (*ImageUploadResult, error) {
		t.Fatal("the handler must not delegate without a file")
		return nil, nil
	})

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	require.NoError(t, writer.WriteField("engine", "vllm"))
	require.NoError(t, writer.Close())
	req := httptest.NewRequest(http.MethodPost, ImageUploadPath, &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	rec := httptest.NewRecorder()
	s.imageUploadHandler(rec, req)
	assert.Equal(t, http.StatusInternalServerError, rec.Code, "business errors use HTTP 500")
	assert.Contains(t, rec.Body.String(), "needs a file")
}

// TestImageUploadHandlerRejectsNonMultipart verifies a JSON body is refused
// with the upload contract.
func TestImageUploadHandlerRejectsNonMultipart(t *testing.T) {
	s := &commonServer{}
	s.SetImageUpload(func(context.Context, ImageUploadRequest) (*ImageUploadResult, error) {
		return nil, nil
	})
	req := httptest.NewRequest(http.MethodPost, ImageUploadPath, bytes.NewReader([]byte(`{"engine":"vllm"}`)))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	s.imageUploadHandler(rec, req)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Contains(t, rec.Body.String(), "multipart/form-data")
}

// TestImageUploadHandlerMapsBusinessError verifies a module error keeps its
// business code in the envelope.
func TestImageUploadHandlerMapsBusinessError(t *testing.T) {
	s := &commonServer{}
	s.SetImageUpload(func(context.Context, ImageUploadRequest) (*ImageUploadResult, error) {
		return nil, apierrors.New(apierrors.CodeImageExists)
	})

	rec := httptest.NewRecorder()
	s.imageUploadHandler(rec, uploadRequest(t, []byte("tar"), map[string]string{"engine": "vllm"}))

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.EqualValues(t, apierrors.CodeImageExists, int(body["code"].(float64)))
}

// TestImageUploadHandlerUnconfigured verifies the endpoint is honest when the
// wiring never installed an uploader.
func TestImageUploadHandlerUnconfigured(t *testing.T) {
	s := &commonServer{}
	rec := httptest.NewRecorder()
	s.imageUploadHandler(rec, uploadRequest(t, []byte("tar"), nil))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Contains(t, rec.Body.String(), "not configured")
}

// TestStageUploadKeepsExtension verifies the staged file name hints at the
// archive type, so an operator inspecting the temp directory can tell what it
// was.
func TestStageUploadKeepsExtension(t *testing.T) {
	path, err := stageUpload(bytes.NewReader([]byte("x")), "engine-image.tar.gz")
	require.NoError(t, err)
	defer func() { _ = os.Remove(path) }()
	assert.Contains(t, filepath.Base(path), ".tar.gz")
}

// metadataAuthorization reads the authorization pair the upload context
// carries, the way a session resolver does.
func metadataAuthorization(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	values := md.Get("authorization")
	if len(values) == 0 {
		return ""
	}
	return values[0]
}
