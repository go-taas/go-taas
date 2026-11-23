// Package modelarchive extracts model weight archives for the upload path.
//
// The same bounded-unpack routine is used by the control plane's local-upload
// fallback and by the Kubernetes Job that turns a chunked upload into a
// catalog model. Keeping it here means the Job does not import the whole model
// service and the two paths cannot drift.
package modelarchive

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

// Unpack limits. The archive is streamed, so the bounds only stop runaway or
// hostile archives from filling the weights filesystem.
const (
	// MaxExtractedBytes caps the total size of all extracted files.
	MaxExtractedBytes = int64(4) << 40
	// MaxFiles caps the entry count of one archive.
	MaxFiles = 200000
	// MaxEntryBytes caps one extracted file.
	MaxEntryBytes = int64(512) << 30

	formatTar   = "tar"
	formatTarGz = "tar.gz"
	formatZip   = "zip"
)

// Format returns the unpack format for an archive file.
func Format(archivePath, originalName string) (string, error) {
	name := strings.ToLower(strings.TrimSpace(originalName))
	if name == "" {
		name = strings.ToLower(archivePath)
	}
	switch {
	case strings.HasSuffix(name, ".zip"):
		return formatZip, nil
	case strings.HasSuffix(name, ".tar.gz"), strings.HasSuffix(name, ".tgz"):
		return formatTarGz, nil
	case strings.HasSuffix(name, ".tar"):
		return formatTar, nil
	default:
		return "", errors.New("model archive must be a .tar, .tar.gz, .tgz or .zip file")
	}
}

// Unpack extracts an archive into dest and returns the number of bytes and
// files written. Every entry is confined to dest; an absolute path, a ".."
// segment or a symlink that escapes the destination is an error.
func Unpack(archivePath, format, dest string) (int64, int, error) {
	file, err := os.Open(filepath.Clean(archivePath))
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = file.Close() }()

	switch format {
	case formatZip:
		info, statErr := file.Stat()
		if statErr != nil {
			return 0, 0, statErr
		}
		return unpackZip(file, info.Size(), dest)
	case formatTarGz:
		gz, gzErr := gzip.NewReader(file)
		if gzErr != nil {
			return 0, 0, errors.New("model archive is not valid gzip: " + gzErr.Error())
		}
		defer func() { _ = gz.Close() }()
		return unpackTar(gz, dest)
	default:
		return unpackTar(file, dest)
	}
}

func unpackTar(src io.Reader, dest string) (int64, int, error) {
	reader := tar.NewReader(src)
	var total int64
	files := 0
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return total, files, nil
		}
		if err != nil {
			return total, files, errors.New("model archive is not a valid tar: " + err.Error())
		}
		if header.Typeflag == tar.TypeDir {
			if _, err := safeJoin(dest, header.Name); err != nil {
				return total, files, err
			}
			continue
		}
		if header.Typeflag != tar.TypeReg {
			return total, files, fmt.Errorf("model archive contains an unsupported entry %q (only files and directories are allowed)", header.Name)
		}
		files++
		if files > MaxFiles {
			return total, files, errors.New("model archive contains too many files")
		}
		if header.Size > MaxEntryBytes || total+header.Size > MaxExtractedBytes {
			return total, files, errors.New("model archive expands beyond the extraction limit")
		}
		written, err := writeEntry(dest, header.Name, reader)
		if err != nil {
			return total, files, err
		}
		total += written
	}
}

func unpackZip(file *os.File, size int64, dest string) (int64, int, error) {
	reader, err := zip.NewReader(file, size)
	if err != nil {
		return 0, 0, errors.New("model archive is not a valid zip: " + err.Error())
	}
	var total int64
	files := 0
	for _, entry := range reader.File {
		if _, err := safeJoin(dest, entry.Name); err != nil {
			return total, files, err
		}
		if entry.FileInfo().IsDir() {
			continue
		}
		if entry.Mode()&os.ModeSymlink != 0 {
			return total, files, errors.New("model archive contains a symlink, which is not allowed")
		}
		files++
		if files > MaxFiles {
			return total, files, errors.New("model archive contains too many files")
		}
		//nolint:gosec // G115: zip header size is bounded by the archive's actual size
		size := int64(entry.UncompressedSize64)
		if size > MaxEntryBytes || total+size > MaxExtractedBytes {
			return total, files, errors.New("model archive expands beyond the extraction limit")
		}
		opened, openErr := entry.Open()
		if openErr != nil {
			return total, files, errors.New("model archive entry cannot be read: " + openErr.Error())
		}
		written, writeErr := writeEntry(dest, entry.Name, opened)
		_ = opened.Close()
		if writeErr != nil {
			return total, files, writeErr
		}
		total += written
	}
	return total, files, nil
}

func writeEntry(dest, name string, src io.Reader) (int64, error) {
	target, err := safeJoin(dest, name)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		return 0, err
	}
	// 0644 matches what the hub downloader writes: the weights directory is a
	// shared read-only mount for engine pods, which may not run as this uid.
	out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644) //nolint:gosec // target is confined by safeJoin
	if err != nil {
		return 0, err
	}
	written, copyErr := io.Copy(out, io.LimitReader(src, MaxEntryBytes))
	closeErr := out.Close()
	if copyErr != nil {
		return written, copyErr
	}
	if closeErr != nil {
		return written, closeErr
	}
	return written, nil
}

func safeJoin(dest, name string) (string, error) {
	clean := strings.ReplaceAll(name, "\\", "/")
	for _, segment := range strings.Split(clean, "/") {
		if segment == ".." {
			return "", fmt.Errorf("model archive entry %q escapes the weight directory", name)
		}
	}
	cleaned := path.Clean("/" + clean)
	target := filepath.Join(dest, filepath.FromSlash(cleaned))
	root := filepath.Clean(dest)
	if target != root && !strings.HasPrefix(target, root+string(os.PathSeparator)) {
		return "", fmt.Errorf("model archive entry %q escapes the weight directory", name)
	}
	return target, nil
}

// CleanModelDirName turns a catalog name into one safe directory segment.
func CleanModelDirName(name string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-.")
	if out == "" {
		return "model-" + uuid.NewString()[:8]
	}
	return out
}

// SanitizeVersion keeps a version usable as a single path segment.
func SanitizeVersion(version string) string {
	replacer := strings.NewReplacer("/", "-", "\\", "-", "..", "-", " ", "-")
	out := replacer.Replace(strings.TrimSpace(version))
	if out == "" {
		return "upload-" + uuid.NewString()[:8]
	}
	return out
}
