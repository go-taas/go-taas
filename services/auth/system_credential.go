package auth

import (
	"context"
	"time"

	"github.com/google/uuid"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// systemCredentialOrgID is the reserved platform organization that owns
// the synthetic load-test credential (feature #20, AD11). It is not a
// tenant organization and never appears in tenant listings.
const systemCredentialOrgID = "system"

// systemCredentialName is the display name of the synthetic credential.
const systemCredentialName = "platform-load-test"

// seedSystemCredential ensures exactly one is_system API-key row exists
// and caches its plaintext in memory for the load-test runner
// (feature #20, AD11).
//
// The plaintext is never persisted (the api_keys schema has no plaintext
// column, feature #1): it is generated here and held only in this
// process. The stored lookup_hash/salted_hash are refreshed on every
// boot so the row always verifies against the credential this process
// hands to the runner. Rotation on restart is intentional — the
// credential is internal, short-lived state, and re-seeding keeps it in
// lockstep with the running server.
func (s *Service) seedSystemCredential(ctx context.Context, repo *APIKeyRepository) error {
	plaintext, err := GenerateAPIKey()
	if err != nil {
		return apierrors.Wrap(apierrors.CodeInternal, err, "auth: generate system credential")
	}
	digest := KeyDigest(plaintext)
	salt, err := NewSalt()
	if err != nil {
		return apierrors.Wrap(apierrors.CodeInternal, err, "auth: generate system credential salt")
	}
	row := &APIKey{
		ID:             uuid.NewString(),
		Name:           systemCredentialName,
		Prefix:         plaintext[:8],
		LookupHash:     digest,
		Salt:           salt,
		SaltedHash:     HashKey(digest, salt, s.hashParamsFor()),
		OrganizationID: systemCredentialOrgID,
		CreatedAt:      time.Now().UTC(),
		IsSystem:       true,
	}
	if err := repo.UpsertSystemCredential(ctx, row); err != nil {
		return err
	}
	s.systemCredential = plaintext
	return nil
}

// GetSystemCredential returns the plaintext system credential the
// load-test runner presents to the inference endpoint (feature #20,
// AD11). It is implemented as a narrow accessor so the infer module
// never reads the api_keys table directly and never holds a tenant key.
//
// The credential is available only after Migrate has seeded it; before
// that (or when seeding failed) it returns CodeInternal so the caller
// can fail the load test closed.
func (s *Service) GetSystemCredential(_ context.Context) (string, error) {
	if s.systemCredential == "" {
		return "", apierrors.Newf(apierrors.CodeInternal, "auth: system credential not seeded")
	}
	return s.systemCredential, nil
}

// EnsureSystemCredential seeds the synthetic platform credential using
// the service's wired repository. It is the injection point for tests
// and FVT, which construct the service directly instead of running the
// full Migrate hook.
func (s *Service) EnsureSystemCredential(ctx context.Context) error {
	if s.repo == nil {
		return apierrors.Newf(apierrors.CodeInternal, "auth: repository not wired")
	}
	return s.seedSystemCredential(ctx, s.repo)
}
