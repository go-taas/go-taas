// Package auth implements the authentication and authorization service:
// local accounts, API keys, SSO federation, and the gateway-facing key
// verification used to authenticate inference traffic.
//
// Local account password hashing (CreateUser/Login): Argon2id with a
// per-password random salt, stored in a self-describing PHC-style
// string so the parameters travel with the hash.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"

	"github.com/go-taas/go-taas/pkg/config"
)

// passwordSaltLength is the number of fresh random bytes per password
// salt.
const passwordSaltLength = 16

// passwordKeyLen is the length of the raw Argon2id output before base64.
const passwordKeyLen uint32 = 32

// passwordHashPrefix is the PHC-style prefix of a stored password hash.
const passwordHashPrefix = "argon2id"

// HashPassword returns a self-describing Argon2id hash of the password
// with a fresh random salt. The format is
// `argon2id$v=19$m=<MiB>,t=<time>,p=<parallelism>$<salt>$<hash>`, so the
// verification parameters travel with the stored value.
func HashPassword(password string, p config.Argon2Params) (string, error) {
	p = p.WithDefaults()
	salt := make([]byte, passwordSaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	hash := argon2.IDKey([]byte(password), salt, clampUint32(p.Time), argon2MemoryKiB(p.MemoryMiB), clampUint8(p.Parallelism), passwordKeyLen)
	return fmt.Sprintf("%s$v=19$m=%d,t=%d,p=%d$%s$%s",
		passwordHashPrefix, p.MemoryMiB, p.Time, p.Parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash)), nil
}

// VerifyPassword checks a password against a stored Argon2id hash in
// constant time. It parses the parameters from the stored value, so a
// hash produced with different parameters still verifies.
func VerifyPassword(password, stored string) bool {
	parts := strings.Split(stored, "$")
	if len(parts) != 5 || parts[0] != passwordHashPrefix {
		return false
	}
	var memory, time, parallelism int
	if _, err := fmt.Sscanf(parts[2], "m=%d,t=%d,p=%d", &memory, &time, &parallelism); err != nil {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}
	expected, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	computed := argon2.IDKey([]byte(password), salt, clampUint32(time), argon2MemoryKiB(memory), clampUint8(parallelism), passwordKeyLen)
	return subtle.ConstantTimeCompare(computed, expected) == 1
}