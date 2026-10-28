// Archive support for the image-upload flow: a user hands the platform a
// `docker save` archive (optionally gzipped) and the platform pushes it into
// the internal Harbor project.
//
// Only manifest.json is read from the archive, and it is read from the
// stream: nothing is extracted, so a malicious archive cannot write outside
// a temp directory. The push is done by skopeo through the docker-archive
// transport, which reads the same file.

package registry

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// maxManifestBytes bounds the manifest read, so a crafted archive cannot make
// the server allocate without limit.
const maxManifestBytes = 4 << 20

// archiveManifestEntry is the file `docker save` writes with the archive's
// repository tags.
const archiveManifestEntry = "manifest.json"

// archiveManifest is the subset of manifest.json this package needs.
type archiveManifest []struct {
	RepoTags []string `json:"RepoTags"`
}

// ReadArchiveReference returns the first repository tag of a docker-save
// archive ("registry/namespace/name:tag"), which names the image the operator
// packed. An archive without a tag (built with `docker build -o` or an image
// that was never tagged) returns "", and the caller falls back to the form
// fields.
//
// The archive may be gzipped; both forms are read transparently.
func ReadArchiveReference(path string) (string, error) {
	file, err := os.Open(path) //nolint:gosec // the path is the server's own temp file
	if err != nil {
		return "", fmt.Errorf("registry: open archive: %w", err)
	}
	defer func() { _ = file.Close() }()

	var reader io.Reader = file
	// A gzip member starts with 0x1f 0x8b. Peek instead of trusting the file
	// extension: a `.tar` may be gzipped and vice versa.
	header := make([]byte, 2)
	if n, err := io.ReadFull(file, header); err == nil && n == 2 && header[0] == 0x1f && header[1] == 0x8b {
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return "", fmt.Errorf("registry: rewind archive: %w", err)
		}
		gz, err := gzip.NewReader(file)
		if err != nil {
			return "", fmt.Errorf("registry: read archive: %w", err)
		}
		defer func() { _ = gz.Close() }()
		reader = gz
	} else if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("registry: rewind archive: %w", err)
	}

	tr := tar.NewReader(reader)
	for {
		entry, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return "", nil
		}
		if err != nil {
			return "", fmt.Errorf("registry: read archive: %w", err)
		}
		name := strings.TrimPrefix(entry.Name, "./")
		if name != archiveManifestEntry {
			continue
		}
		raw, err := io.ReadAll(io.LimitReader(tr, maxManifestBytes))
		if err != nil {
			return "", fmt.Errorf("registry: read manifest: %w", err)
		}
		var manifest archiveManifest
		if err := json.Unmarshal(raw, &manifest); err != nil {
			return "", fmt.Errorf("registry: parse manifest: %w", err)
		}
		for _, image := range manifest {
			for _, tag := range image.RepoTags {
				if tag != "" && tag != "<none>:<none>" {
					return tag, nil
				}
			}
		}
		return "", nil
	}
}

// ArchiveImporter pushes a docker-save archive into a registry.
type ArchiveImporter interface {
	// ImportArchive pushes the image in the archive at archivePath to
	// dstRef. srcRef selects the archive member ("name:tag"); empty lets
	// skopeo take the archive's own tag.
	ImportArchive(ctx context.Context, archivePath, srcRef, dstRef string, dstCreds *Credentials) error
}

// ImportArchive implements ArchiveImporter using `skopeo copy` with the
// docker-archive transport.
func (s *SkopeoImporter) ImportArchive(ctx context.Context, archivePath, srcRef, dstRef string, dstCreds *Credentials) error {
	src := "docker-archive:" + archivePath
	// The optional ":reference" selects one tag when the archive carries
	// several; without it skopeo copies every tag it finds.
	if strings.TrimSpace(srcRef) != "" {
		src += ":" + srcRef
	}
	args := []string{"copy"}
	if dstCreds != nil && dstCreds.Username != "" {
		args = append(args, "--dest-creds", dstCreds.Username+":"+dstCreds.Password)
	}
	args = append(args, src, "docker://"+dstRef)

	out, err := s.Runner(ctx, "skopeo", args...)
	if err != nil {
		return fmt.Errorf("registry: skopeo push %s -> %s failed: %w: %s",
			archivePath, dstRef, err, strings.TrimSpace(string(out)))
	}
	return nil
}
