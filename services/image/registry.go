package image

import (
	"strings"
	"sync"

	"github.com/go-taas/go-taas/pkg/config"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// Summary is the read model served by the transitional in-memory
// registry. It carries everything the infer module needs to validate a
// deploy request and to compose the change event.
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

// registry is the transitional in-memory image registry seeded from
// configuration. It is replaced by the images table in feature #3; the
// Lookup interface stays stable so the infer module does not change.
type registry struct {
	mu      sync.RWMutex
	entries map[string]*Summary
}

// imageRegistry is the singleton registry instance. It is seeded once
// from the process configuration on first use.
var imageRegistry = &registry{entries: map[string]*Summary{}}

var seedOnce sync.Once

// seedFromConfig loads the image.registry section of the process
// configuration into the singleton registry. It is idempotent.
func seedFromConfig() {
	seedOnce.Do(func() {
		cfg := config.GetConfig()
		if cfg == nil {
			return
		}
		for _, e := range cfg.Image.Registry {
			imageRegistry.entries[e.ImageID] = &Summary{
				ImageID:     e.ImageID,
				Name:        e.Name,
				Tag:         e.Tag,
				Accelerator: e.Accelerator,
				Engine:      e.Engine,
			}
		}
	})
}

// Lookup returns the image summary for imageID. A miss maps to
// CodeImageNotFound.
func Lookup(imageID string) (*Summary, error) {
	seedFromConfig()
	imageRegistry.mu.RLock()
	defer imageRegistry.mu.RUnlock()
	s, ok := imageRegistry.entries[imageID]
	if !ok {
		return nil, apierrors.New(apierrors.CodeImageNotFound)
	}
	return s, nil
}

// List returns all registered images, optionally filtered by accelerator
// and engine (empty filter values match everything).
func List(accelerator, engine string) []*Summary {
	seedFromConfig()
	imageRegistry.mu.RLock()
	defer imageRegistry.mu.RUnlock()

	out := make([]*Summary, 0, len(imageRegistry.entries))
	for _, s := range imageRegistry.entries {
		if accelerator != "" && s.Accelerator != accelerator {
			continue
		}
		if engine != "" && s.Engine != engine {
			continue
		}
		out = append(out, s)
	}
	return out
}

// ResetForTest replaces the registry contents and returns a restore
// function. Test-only: it exists so unit tests can seed the registry
// without touching the process configuration.
func ResetForTest(entries []*Summary) func() {
	imageRegistry.mu.Lock()
	saved := imageRegistry.entries
	imageRegistry.entries = map[string]*Summary{}
	for _, e := range entries {
		imageRegistry.entries[e.ImageID] = e
	}
	imageRegistry.mu.Unlock()
	return func() {
		imageRegistry.mu.Lock()
		imageRegistry.entries = saved
		imageRegistry.mu.Unlock()
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
