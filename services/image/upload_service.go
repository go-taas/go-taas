// Image upload: a user hands the platform a `docker save` archive and the
// platform pushes it into the internal Harbor project, then registers it in
// the catalog.
//
// The upload arrives over HTTP (a multipart stream, see
// pkg/server/image_upload.go), because gRPC-JSON transcoding cannot carry a
// binary body. The service does the validating, pushing and registering, so
// the upload behaves exactly like the registry import: same destination rule
// (the archive's image name lands in the configured project), same
// compatibility rules and the same catalog row.

package image

import (
	"context"
	"strings"

	"github.com/go-taas/go-taas/pkg/config"
	"github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/logger"
	"github.com/go-taas/go-taas/pkg/registry"
	"github.com/go-taas/go-taas/services/audit"
	"github.com/google/uuid"
)

// UploadImageRequest is one image upload.
type UploadImageRequest struct {
	// ArchivePath is the server-side temp file holding the archive.
	ArchivePath string
	// Name optionally names the image inside the project. Empty derives it
	// from the archive (or from SourceReference).
	Name string
	// Tag optionally tags the image. Empty derives it from the archive.
	Tag string
	// SourceReference is the archive's image reference as the user typed it
	// ("registry/name:tag"), used when the archive carries no tag.
	SourceReference string
	// Accelerator and Engine classify the image; both are required.
	Accelerator string
	Engine      string
	Description string
	// Actor is the account that uploaded the image, for the audit trail.
	Actor string
}

// UploadImageResult is the registered catalog entry.
type UploadImageResult struct {
	ImageID   string
	Reference string
	Name      string
	Tag       string
}

// UploadImage pushes an uploaded archive into the internal Harbor project and
// registers it. The destination is the configured project; only the image's
// own name is kept, exactly like the registry import, so an upload never
// reproduces the archive's namespace tree.
//
// Validation order: platform administrator, Harbor configured, importer
// wired, accelerator, engine, description, archive readable. Nothing is
// pushed and nothing is written when a check fails.
func (s *Service) UploadImage(ctx context.Context, req UploadImageRequest) (*UploadImageResult, error) {
	if err := s.requirePlatformAdmin(ctx); err != nil {
		return nil, err
	}
	accelerator := strings.ToLower(strings.TrimSpace(req.Accelerator))
	engine := strings.TrimSpace(req.Engine)
	description := strings.TrimSpace(req.Description)
	if !IsValidImageAccelerator(accelerator) {
		return nil, errors.New(errors.CodeImageIncompatible)
	}
	if accelerator == "" {
		accelerator = AcceleratorCPU
	}
	if len(engine) < 1 || len(engine) > maxEngineLen {
		return nil, errors.Newf(errors.CodeImageReferenceInvalid, "image: engine must be 1-%d characters", maxEngineLen)
	}
	if len(description) > maxDescriptionLen {
		return nil, errors.Newf(errors.CodeImageReferenceInvalid, "image: description must be at most %d characters", maxDescriptionLen)
	}
	if req.ArchivePath == "" {
		return nil, errors.Newf(errors.CodeImageReferenceInvalid, "image: the uploaded archive is empty")
	}

	cfg := config.GetConfig()
	if cfg == nil || cfg.Image.Harbor.URL == "" {
		return nil, errors.Newf(errors.CodeImageReferenceInvalid, "image: internal Harbor registry is not configured")
	}
	archiveImporter, ok := s.importer.(registry.ArchiveImporter)
	if s.importer == nil || !ok {
		return nil, errors.Newf(errors.CodeImageReferenceInvalid, "image: image upload is not configured")
	}

	// The archive names the image it contains; the form fields are the
	// fallback for an archive whose image was never tagged.
	archiveRef, err := registry.ReadArchiveReference(req.ArchivePath)
	if err != nil {
		return nil, errors.Newf(errors.CodeImageReferenceInvalid, "image: %v", err)
	}
	sourceRef := strings.TrimSpace(archiveRef)
	if sourceRef == "" {
		sourceRef = strings.TrimSpace(req.SourceReference)
	}
	name := strings.TrimSpace(req.Name)
	tag := strings.TrimSpace(req.Tag)
	if sourceRef != "" {
		_, repo, refTag, parseErr := registry.ParseReference(sourceRef)
		if parseErr != nil {
			return nil, errors.Newf(errors.CodeImageReferenceInvalid, "image: %v", parseErr)
		}
		if name == "" {
			name = registry.LastRepoName(repo)
		}
		if tag == "" {
			tag = refTag
		}
	}
	if name == "" {
		return nil, errors.Newf(errors.CodeImageReferenceInvalid,
			"image: cannot derive the image name; name it in the form or tag the image before saving the archive")
	}
	if tag == "" {
		tag = "latest"
	}
	if !namePattern.MatchString(name) || strings.Contains(name, ":") || len(name) > maxNameLen {
		return nil, errors.Newf(errors.CodeImageReferenceInvalid,
			"image: name must be a lowercase reference without tag, 1-%d characters", maxNameLen)
	}

	localRepo := cfg.Image.Harbor.Project + "/" + name
	destRef := registry.JoinReference(cfg.Image.Harbor.URL, localRepo, tag)
	catalogName := cfg.Image.Harbor.URL + "/" + localRepo

	repoHandle, err := s.repository()
	if err != nil {
		return nil, err
	}
	if _, err := repoHandle.FindByTriple(ctx, catalogName, tag, accelerator); err == nil {
		return nil, errors.New(errors.CodeImageExists)
	}

	// The archive member is selected by its own reference when one is known,
	// so an archive carrying several tags pushes the intended image; without
	// it skopeo takes the archive's single tag.
	dstCreds := &registry.Credentials{
		Username: cfg.Image.Harbor.Username,
		Password: cfg.Image.Harbor.Password,
	}
	if err := archiveImporter.ImportArchive(ctx, req.ArchivePath, sourceRef, destRef, dstCreds); err != nil {
		return nil, errors.Newf(errors.CodeImageReferenceInvalid, "image: upload failed: %v", err)
	}

	img := &Image{
		ID:          uuid.NewString(),
		Name:        catalogName,
		Tag:         tag,
		Accelerator: accelerator,
		Engine:      engine,
		Description: description,
	}
	if err := repoHandle.CreateImage(ctx, img); err != nil {
		return nil, err
	}
	// Feature #15: record the successful upload best-effort.
	actor := strings.TrimSpace(req.Actor)
	if actor == "" {
		actor = "admin"
	}
	s.recordAudit(ctx, &audit.AuditEvent{
		OrganizationID: "",
		ActorUserID:    actor,
		ActorType:      "user",
		Action:         "image.upload",
		ResourceType:   "image",
		ResourceID:     img.ID,
		Result:         "success",
	})
	logger.S().Infow("image: uploaded archive pushed",
		"image_id", img.ID, "reference", destRef, "archive", req.ArchivePath)

	return &UploadImageResult{
		ImageID:   img.ID,
		Reference: destRef,
		Name:      catalogName,
		Tag:       tag,
	}, nil
}
