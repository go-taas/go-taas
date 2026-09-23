package image

import (
	"context"
	"strings"

	"gorm.io/gorm"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// Summary is the read model served by the DB-backed registry. It
// carries everything the infer module needs to validate a deploy
// request and to compose the change event. The struct is unchanged
// from the transitional in-memory registry so the infer module does
// not change (D1).
type Summary struct {
	// ImageID is the stable image identifier used by deploy requests.
	ImageID string
	// Name is the image repository name (without tag).
	Name string
	// Tag is the image tag.
	Tag string
	// Accelerator names the hardware platform the image runs on.
	Accelerator string
	// Engine names the inference engine (vllm, sglang, ...).
	Engine string
}

// Reference returns the full image reference ("name:tag").
func (s *Summary) Reference() string {
	return s.Name + ":" + s.Tag
}

// lookupDB is the database backing the package-level Lookup/List
// helpers. It is wired by wireRegistry at service construction (the
// database component is initialized by server Init, which runs after
// service construction).
var lookupDB *gorm.DB

// wireRegistry binds the package-level Lookup/List helpers to a
// database. Called by the image service when it resolves its
// repositories; tests call it with a disposable database.
func wireRegistry(db *gorm.DB) {
	lookupDB = db
}

// WireRegistryForTest binds the package-level Lookup/List helpers to a
// caller-provided database. It exists for cross-module tests (infer)
// that seed a disposable registry without constructing the image
// service.
func WireRegistryForTest(db *gorm.DB) {
	wireRegistry(db)
}

// Lookup returns the image summary for imageID. A miss maps to
// CodeImageNotFound. The signature is unchanged from the transitional
// in-memory registry (D1).
func Lookup(imageID string) (*Summary, error) {
	if lookupDB == nil {
		return nil, apierrors.Newf(apierrors.CodeInternal, "image: registry not wired")
	}
	repo := NewRepository(lookupDB)
	img, err := repo.FindByID(context.Background(), imageID)
	if err != nil {
		return nil, err
	}
	return imageToSummary(img), nil
}

// List returns registered images, optionally filtered by accelerator
// and engine (empty filter values match everything). The signature is
// unchanged from the transitional in-memory registry (D1).
func List(accelerator, engine string) []*Summary {
	if lookupDB == nil {
		return []*Summary{}
	}
	repo := NewRepository(lookupDB)
	rows, _, err := repo.ListImages(context.Background(), accelerator, engine, 0, maxListAll)
	if err != nil {
		return []*Summary{}
	}
	out := make([]*Summary, 0, len(rows))
	for _, img := range rows {
		out = append(out, imageToSummary(img))
	}
	return out
}

// maxListAll bounds the unpaginated List helper. The catalog is small;
// the bound is a safety valve, not an expected limit.
const maxListAll = 10000

// imageToSummary maps a DB row to the infer-facing read model.
func imageToSummary(img *Image) *Summary {
	return &Summary{
		ImageID:     img.ID,
		Name:        img.Name,
		Tag:         img.Tag,
		Accelerator: img.Accelerator,
		Engine:      img.Engine,
	}
}

// IsValidAccelerator reports whether the accelerator is in the supported
// set (nvidia, iluvatar, metax).
func IsValidAccelerator(accelerator string) bool {
	switch strings.ToLower(accelerator) {
	case "nvidia", "iluvatar", "metax":
		return true
	default:
		return false
	}
}
