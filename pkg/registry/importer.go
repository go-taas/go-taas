// Package registry copies container images between registries. It is
// used by the image-import flow to pull an engine image from a source
// registry and push it into the internal Harbor project.
package registry

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// Credentials holds a registry account.
type Credentials struct {
	// Username is the registry account name.
	Username string
	// Password is the registry account password.
	Password string
}

// Importer copies a container image from a source reference to a
// destination reference.
type Importer interface {
	// Import copies the image at srcRef to dstRef, authenticating with
	// the given credentials. srcCreds may be empty for public sources.
	Import(ctx context.Context, srcRef string, srcCreds *Credentials, dstRef string, dstCreds *Credentials) error
}

// CommandRunner runs a command and returns its combined output. It is
// the seam tests use to inject a fake skopeo.
type CommandRunner func(ctx context.Context, name string, args ...string) ([]byte, error)

// DefaultCommandRunner runs the command via os/exec.
func DefaultCommandRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// SkopeoImporter is the default Importer backed by the skopeo CLI.
type SkopeoImporter struct {
	// Runner executes skopeo. Defaults to DefaultCommandRunner.
	Runner CommandRunner
}

// NewSkopeoImporter builds a SkopeoImporter.
func NewSkopeoImporter() *SkopeoImporter {
	return &SkopeoImporter{Runner: DefaultCommandRunner}
}

// Import implements Importer using `skopeo copy`.
func (s *SkopeoImporter) Import(ctx context.Context, srcRef string, srcCreds *Credentials, dstRef string, dstCreds *Credentials) error {
	args := []string{"copy"}
	if srcCreds != nil && srcCreds.Username != "" {
		args = append(args, "--src-creds", srcCreds.Username+":"+srcCreds.Password)
	}
	if dstCreds != nil && dstCreds.Username != "" {
		args = append(args, "--dest-creds", dstCreds.Username+":"+dstCreds.Password)
	}
	args = append(args, "docker://"+srcRef, "docker://"+dstRef)

	out, err := s.Runner(ctx, "skopeo", args...)
	if err != nil {
		return fmt.Errorf("registry: skopeo copy %s -> %s failed: %w: %s", srcRef, dstRef, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ParseReference splits a container image reference into its registry
// host, repository and tag. It returns the host, the repository (with
// optional namespace) and the tag. A missing tag defaults to "latest".
func ParseReference(ref string) (host, repo, tag string, err error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", "", "", fmt.Errorf("registry: empty image reference")
	}
	// Split off the tag.
	tag = "latest"
	if i := strings.LastIndex(ref, ":"); i >= 0 && !strings.Contains(ref[i:], "/") {
		tag = ref[i+1:]
		ref = ref[:i]
	}
	// Split off the registry host (the part before the first "/" that
	// contains a "." or ":" or is "localhost").
	slash := strings.Index(ref, "/")
	if slash < 0 {
		// No host: it is a bare repository on Docker Hub.
		return "", ref, tag, nil
	}
	first := ref[:slash]
	if strings.Contains(first, ".") || strings.Contains(first, ":") || first == "localhost" {
		return first, ref[slash+1:], tag, nil
	}
	// The first segment is a namespace, not a host (Docker Hub).
	return "", ref, tag, nil
}

// LastRepoName returns the final path segment of a repository: the image
// name without its registry host and namespaces. Importing
// "docker.io/library/a/b/hello:1.0.0" keeps "hello", so the local catalog
// reads "<harbor>/<project>/hello:1.0.0" instead of reproducing the source
// registry's namespace tree.
func LastRepoName(repo string) string {
	trimmed := strings.Trim(repo, "/")
	if i := strings.LastIndex(trimmed, "/"); i >= 0 {
		return trimmed[i+1:]
	}
	return trimmed
}

// JoinReference composes a full image reference from a host, repository
// and tag.
func JoinReference(host, repo, tag string) string {
	if host == "" {
		return repo + ":" + tag
	}
	return host + "/" + repo + ":" + tag
}
