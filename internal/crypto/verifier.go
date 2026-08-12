package crypto

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

// CredentialVerifier verifies stored Argon2id credentials while allowing the
// application to share one bounded work budget across authentication surfaces.
type CredentialVerifier interface {
	Verify(ctx context.Context, rawValue, encodedHash string) (bool, error)
}

// Argon2Verifier is safe for concurrent use. A nil slots channel means callers
// still get strict hash validation without a concurrency limit, which is useful
// for isolated package tests and compatibility constructors.
type Argon2Verifier struct {
	slots chan struct{}
}

var _ CredentialVerifier = (*Argon2Verifier)(nil)

func NewArgon2Verifier(limit int) *Argon2Verifier {
	if limit <= 0 {
		return &Argon2Verifier{}
	}
	return &Argon2Verifier{slots: make(chan struct{}, limit)}
}

func (v *Argon2Verifier) Verify(ctx context.Context, rawValue, encodedHash string) (bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if v != nil && v.slots != nil {
		select {
		case v.slots <- struct{}{}:
			defer func() { <-v.slots }()
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return verifyArgon2id(rawValue, encodedHash), nil
}

func verifyArgon2id(rawValue, encodedHash string) bool {
	parts := strings.Split(encodedHash, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != "v=19" {
		return false
	}
	params := map[string]uint32{}
	for _, item := range strings.Split(parts[3], ",") {
		pieces := strings.SplitN(item, "=", 2)
		if len(pieces) != 2 || pieces[0] == "" {
			return false
		}
		value, err := strconv.ParseUint(pieces[1], 10, 32)
		if err != nil {
			return false
		}
		if _, exists := params[pieces[0]]; exists {
			return false
		}
		params[pieces[0]] = uint32(value)
	}
	if len(params) != 3 || params["m"] != argon2Memory || params["t"] != argon2Time || params["p"] != argon2Threads {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) != argon2SaltLength {
		return false
	}
	expectedHash, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(expectedHash) != argon2KeyLength {
		return false
	}
	computedHash := argon2.IDKey([]byte(rawValue), salt, argon2Time, argon2Memory, argon2Threads, argon2KeyLength)
	return subtle.ConstantTimeCompare(computedHash, expectedHash) == 1
}

// VerifyKey preserves the package's historical boolean API while using the
// strict PHC validation shared by runtime authentication.
func VerifyKey(rawValue, encodedHash string) bool {
	valid, err := NewArgon2Verifier(0).Verify(context.Background(), rawValue, encodedHash)
	return err == nil && valid
}
