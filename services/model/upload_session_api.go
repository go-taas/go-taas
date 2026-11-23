package model

import (
	"context"
	"io"

	"github.com/go-taas/go-taas/pkg/server"
)

// modelUploadView projects one session row for the console.
func modelUploadView(row *ModelUpload) *server.ModelUploadSessionView {
	return &server.ModelUploadSessionView{
		UploadID:       row.ID,
		Name:           row.Name,
		HubModelID:     row.HubModelID,
		Version:        row.Version,
		Description:    row.Description,
		StorageBackend: row.StorageBackend,
		ArchiveName:    row.ArchiveName,
		TotalBytes:     row.TotalBytes,
		PartSizeBytes:  row.PartSizeBytes,
		State:          row.State,
		BytesReceived:  row.BytesReceived,
		ModelID:        row.ModelID,
		Message:        row.Message,
		CreatedAt:      row.CreatedAt.Format("2006-01-02T15:04:05Z"),
		UpdatedAt:      row.UpdatedAt.Format("2006-01-02T15:04:05Z"),
	}
}

// CreateUploadSession implements server.ModelUploadSessionAPI.
func (s *Service) CreateUploadSession(ctx context.Context, req server.ModelUploadCreateRequest) (*server.ModelUploadSessionView, error) {
	row, err := s.createUploadSessionRow(ctx, uploadCreateRequest{
		Name: req.Name, HubModelID: req.HubModelID, Description: req.Description,
		StorageBackend: req.StorageBackend, ArchiveName: req.ArchiveName, TotalBytes: req.TotalBytes,
	})
	if err != nil {
		return nil, err
	}
	return modelUploadView(row), nil
}

// GetUploadSession implements server.ModelUploadSessionAPI.
func (s *Service) GetUploadSession(ctx context.Context, uploadID string) (*server.ModelUploadSessionView, error) {
	row, err := s.getUploadSessionRow(ctx, uploadID)
	if err != nil {
		return nil, err
	}
	return modelUploadView(row), nil
}

// ListUploadSessions implements server.ModelUploadSessionAPI.
func (s *Service) ListUploadSessions(ctx context.Context, limit int) ([]*server.ModelUploadSessionView, error) {
	rows, err := s.listUploadSessionsRow(ctx, limit)
	if err != nil {
		return nil, err
	}
	views := make([]*server.ModelUploadSessionView, 0, len(rows))
	for _, r := range rows {
		views = append(views, modelUploadView(r))
	}
	return views, nil
}

// ListUploadedParts implements server.ModelUploadSessionAPI.
func (s *Service) ListUploadedParts(ctx context.Context, uploadID string) ([]int, error) {
	return s.listUploadedPartsRow(ctx, uploadID)
}

// UploadPart implements server.ModelUploadSessionAPI.
func (s *Service) UploadPart(ctx context.Context, uploadID string, partNumber int, body io.Reader, size int64) error {
	_, err := s.uploadPartRow(ctx, uploadPartRequest{UploadID: uploadID, PartNumber: partNumber, Body: body, Size: size})
	return err
}

// CompleteUploadSession implements server.ModelUploadSessionAPI.
func (s *Service) CompleteUploadSession(ctx context.Context, uploadID string) (*server.ModelUploadSessionView, error) {
	row, err := s.completeUploadSessionRow(ctx, uploadID)
	if err != nil {
		return nil, err
	}
	return modelUploadView(row), nil
}
