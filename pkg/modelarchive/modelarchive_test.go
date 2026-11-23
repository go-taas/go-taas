package modelarchive

import (
	"archive/tar"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFormat(t *testing.T) {
	cases := map[string]string{
		"a.zip":    formatZip,
		"a.tgz":    formatTarGz,
		"a.tar.gz": formatTarGz,
		"a.tar":    formatTar,
		"anything": "",
		"A.TAR.GZ": formatTarGz, // case-insensitive
	}
	for name, want := range cases {
		got, _ := Format("", name)
		if got != want {
			t.Errorf("Format(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestUnpackTar(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "m.tar")
	writeTestTar(t, archive, "a.txt", "hello", "nested/b.bin", "b")

	dest := filepath.Join(dir, "out")
	bytes, files, err := Unpack(archive, formatTar, dest)
	if err != nil {
		t.Fatalf("unpack: %v", err)
	}
	if bytes != 6 {
		t.Errorf("bytes = %d, want 6", bytes)
	}
	if files != 2 {
		t.Errorf("files = %d, want 2", files)
	}
	for _, want := range []string{"a.txt", "nested/b.bin"} {
		p := filepath.Join(dest, filepath.FromSlash(want))
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s missing: %v", want, err)
		}
	}
}

func TestUnpackRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "m.tar")
	writeTestTar(t, archive, "../escape", "x")

	if _, _, err := Unpack(archive, formatTar, filepath.Join(dir, "out")); err == nil {
		t.Fatal("expected traversal to be rejected")
	}
}

func TestCleanModelDirName(t *testing.T) {
	if got := CleanModelDirName("My Model v2"); got != "My-Model-v2" {
		t.Errorf("got %q", got)
	}
	if got := CleanModelDirName("  "); !strings.HasPrefix(got, "model-") {
		t.Errorf("empty name should fall back, got %q", got)
	}
}

func TestSanitizeVersion(t *testing.T) {
	if got := SanitizeVersion("a/b\\c..d e"); got != "a-b-c-d-e" {
		t.Errorf("got %q", got)
	}
	if got := SanitizeVersion(""); !strings.HasPrefix(got, "upload-") {
		t.Errorf("empty version should fall back, got %q", got)
	}
}

func writeTestTar(t *testing.T, path string, args ...string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	tw := tar.NewWriter(f)
	defer func() { _ = tw.Close() }()
	for i := 0; i+1 < len(args); i += 2 {
		name, content := args[i], args[i+1]
		if name == "" {
			continue
		}
		if name[len(name)-1] == '/' {
			if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(content))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
}
