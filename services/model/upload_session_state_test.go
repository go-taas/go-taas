package model

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestUploadStateReportsAStaleUploadAsInterrupted(t *testing.T) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name string
		row  *ModelUpload
		want string
	}{
		{
			name: "fresh uploading stays uploading",
			row:  &ModelUpload{State: UploadStateUploading, UpdatedAt: now.Add(-time.Second)},
			want: UploadStateUploading,
		},
		{
			name: "an upload idle past the stale window is interrupted",
			row:  &ModelUpload{State: UploadStateUploading, UpdatedAt: now.Add(-2 * uploadStaleAfter)},
			want: UploadStateInterrupted,
		},
		{
			name: "stale creating is interrupted too",
			row:  &ModelUpload{State: UploadStateCreating, UpdatedAt: now.Add(-2 * uploadStaleAfter)},
			want: UploadStateInterrupted,
		},
		{
			name: "an interrupted session stays interrupted",
			row:  &ModelUpload{State: UploadStateInterrupted, UpdatedAt: now},
			want: UploadStateInterrupted,
		},
		{
			name: "a stale processing upload is still processing",
			row:  &ModelUpload{State: UploadStateProcessing, UpdatedAt: now.Add(-time.Hour)},
			want: UploadStateProcessing,
		},
		{
			name: "a succeeded upload stays succeeded",
			row:  &ModelUpload{State: UploadStateSucceeded, UpdatedAt: now.Add(-time.Hour)},
			want: UploadStateSucceeded,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, uploadState(tc.row, now))
		})
	}
}
