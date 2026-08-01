// Package gate evaluates Public Shares entry credentials. It is pure policy:
// no sessions, cookies, filesystem or network access.
package gate

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"filees/public-shares/channel"
	"golang.org/x/crypto/argon2"
)

const Anonymous = "anonymous"

var (
	ErrDenied          = errors.New("public share access denied")
	ErrInvalidPassword = errors.New("public share password verifier is invalid")
)

type Principal struct {
	Recipient string
}

// TokenHash is the only token representation stored in public state.
func TokenHash(token string) string {
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}

// Authorize applies exactly one gate. Closed channels accept only a recipient
// token; open channels accept their password when configured, otherwise no
// credential. All token digests are compared even after a match.
func Authorize(projection channel.Projection, token, password string) (Principal, error) {
	if err := projection.Validate(); err != nil {
		return Principal{}, ErrDenied
	}
	if len(projection.Recipients) > 0 {
		got := sha256.Sum256([]byte(token))
		matched := -1
		for i, recipient := range projection.Recipients {
			want, err := hex.DecodeString(recipient.TokenHash)
			if err != nil || len(want) != sha256.Size {
				return Principal{}, ErrDenied
			}
			if subtle.ConstantTimeCompare(got[:], want) == 1 {
				matched = i
			}
		}
		if matched < 0 {
			return Principal{}, ErrDenied
		}
		return Principal{Recipient: projection.Recipients[matched].Email}, nil
	}
	if projection.PasswordHash != "" {
		ok, err := VerifyPassword(projection.PasswordHash, password)
		if err != nil || !ok {
			return Principal{}, ErrDenied
		}
	}
	return Principal{Recipient: Anonymous}, nil
}

// HashPassword emits a bounded Argon2id PHC string. The caller must keep the
// plaintext out of any SVN-backed ticket; only this verifier belongs in a
// manifest.
func HashPassword(password string, source io.Reader) (string, error) {
	if len(password) < 8 || len(password) > 1024 || strings.IndexByte(password, 0) >= 0 {
		return "", errors.New("public share password must contain 8 to 1024 bytes and no NUL")
	}
	if source == nil {
		source = rand.Reader
	}
	salt := make([]byte, 16)
	if _, err := io.ReadFull(source, salt); err != nil {
		return "", err
	}
	const memory, iterations, threads = uint32(64 * 1024), uint32(3), uint8(1)
	digest := argon2.IDKey([]byte(password), salt, iterations, memory, threads, 32)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, memory, iterations, threads, base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(digest)), nil
}

func VerifyPassword(encoded, password string) (bool, error) {
	params, salt, want, err := parse(encoded)
	if err != nil {
		return false, err
	}
	got := argon2.IDKey([]byte(password), salt, params.iterations, params.memory, params.threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

type parameters struct {
	memory, iterations uint32
	threads            uint8
}

func parse(encoded string) (parameters, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != "v=19" {
		return parameters{}, nil, nil, ErrInvalidPassword
	}
	values := strings.Split(parts[3], ",")
	if len(values) != 3 {
		return parameters{}, nil, nil, ErrInvalidPassword
	}
	memory, errM := parseUint(values[0], "m=", 32)
	iterations, errT := parseUint(values[1], "t=", 32)
	threads, errP := parseUint(values[2], "p=", 8)
	if errM != nil || errT != nil || errP != nil || memory < 8*1024 || memory > 128*1024 || iterations < 1 || iterations > 8 || threads < 1 || threads > 8 {
		return parameters{}, nil, nil, ErrInvalidPassword
	}
	salt, err := base64.RawStdEncoding.Strict().DecodeString(parts[4])
	if err != nil || len(salt) < 16 || len(salt) > 64 {
		return parameters{}, nil, nil, ErrInvalidPassword
	}
	digest, err := base64.RawStdEncoding.Strict().DecodeString(parts[5])
	if err != nil || len(digest) != 32 {
		return parameters{}, nil, nil, ErrInvalidPassword
	}
	return parameters{memory: uint32(memory), iterations: uint32(iterations), threads: uint8(threads)}, salt, digest, nil
}

func parseUint(value, prefix string, bits int) (uint64, error) {
	if !strings.HasPrefix(value, prefix) {
		return 0, ErrInvalidPassword
	}
	parsed, err := strconv.ParseUint(strings.TrimPrefix(value, prefix), 10, bits)
	if err != nil {
		return 0, ErrInvalidPassword
	}
	return parsed, nil
}
