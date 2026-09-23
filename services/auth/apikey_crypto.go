package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"math"

	"golang.org/x/crypto/argon2"

	"github.com/go-taas/go-taas/pkg/config"
)

// API key format constants: sk- + 43 base62 characters (~256 bits of
// entropy, 46 chars total), matching the OpenAI-compatible ecosystem
// convention that agents and SDKs already expect.
const (
	apiKeyScheme       = "sk-"
	apiKeySecretLength = 43
	// apiKeySaltLength is the number of fresh random bytes per key salt.
	apiKeySaltLength = 16
	kiBPerMiB        = 1024
)

// base62Alphabet is the uniform alphabet used for key secrets.
const base62Alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// argon2KeyLen is the length of the raw Argon2id output before base64.
const argon2KeyLen uint32 = 32

// GenerateAPIKey returns a fresh plaintext API key of the form
// sk-<43 base62 chars>. The 43 characters are drawn uniformly from
// crypto/rand via per-character rejection sampling, so the key carries
// ~256 bits of entropy and is unique by construction.
func GenerateAPIKey() (string, error) {
	buf := make([]byte, apiKeySecretLength)
	for i := range buf {
		// Rejection sampling: draw a byte and reject values that would
		// introduce modulo bias over the 62-symbol alphabet.
		for {
			var b [1]byte
			if _, err := rand.Read(b[:]); err != nil {
				return "", err
			}
			// 248 = 62*4: bytes in [0,248) map uniformly onto the
			// alphabet; the remaining 8 values are rejected.
			if b[0] < 62*4 {
				buf[i] = base62Alphabet[int(b[0])%62]
				break
			}
		}
	}
	return apiKeyScheme + string(buf), nil
}

// KeyDigest returns the lowercase hex SHA-256 of the plaintext key. This
// is the contract shared with the data-plane Wasm plugin: the gateway
// computes the digest from the Authorization header and sends only the
// digest, so the plaintext never leaves the gateway.
func KeyDigest(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

// IsValidDigest reports whether digest is a well-formed key digest: 64
// lowercase hex characters.
func IsValidDigest(digest string) bool {
	if len(digest) != 64 {
		return false
	}
	for _, c := range digest {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// NewSalt returns the base64 of 16 fresh random bytes, one per key.
func NewSalt() (string, error) {
	b := make([]byte, apiKeySaltLength)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

// HashKey computes the base64 Argon2id hash of the key digest with the
// given salt. The Argon2id input is the 64-char hex digest string's
// bytes (pinned to avoid a decode-step mismatch with the gateway).
func HashKey(digest, salt string, p config.Argon2Params) string {
	p = p.WithDefaults()
	saltBytes, _ := base64.StdEncoding.DecodeString(salt) // salt is always our own base64
	hash := argon2.IDKey([]byte(digest), saltBytes, clampUint32(p.Time), argon2MemoryKiB(p.MemoryMiB), clampUint8(p.Parallelism), argon2KeyLen)
	return base64.StdEncoding.EncodeToString(hash)
}

// clampUint32 clamps v into the uint32 range.
func clampUint32(v int) uint32 {
	if v < 0 {
		return 0
	}
	if v > math.MaxUint32 {
		return math.MaxUint32
	}
	return uint32(v)
}

// clampUint8 clamps v into the uint8 range.
func clampUint8(v int) uint8 {
	if v < 0 {
		return 0
	}
	if v > math.MaxUint8 {
		return math.MaxUint8
	}
	return uint8(v)
}

func argon2MemoryKiB(memoryMiB int) uint32 {
	kiB := memoryMiB * kiBPerMiB
	if kiB < 0 || kiB > math.MaxUint32 {
		return math.MaxUint32
	}
	return uint32(kiB)
}

// VerifyKeyHash recomputes the Argon2id hash of digest and compares it
// with expected in constant time.
func VerifyKeyHash(digest, salt, expected string, p config.Argon2Params) bool {
	computed := HashKey(digest, salt, p)
	return subtle.ConstantTimeCompare([]byte(computed), []byte(expected)) == 1
}
