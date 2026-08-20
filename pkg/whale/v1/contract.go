// Package v1 defines the transport-neutral identity and state contract for
// Whale objects. It deliberately knows nothing about GUI, SSH or SVN: those
// are adapters around the same logical repository, path and generation.
package v1

import (
	"errors"
	"fmt"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	Schema                    = "filees.whale/v1"
	GenerationSchema          = "filees.whale-generation/v1"
	ReservedNamespace         = ".filees-whales"
	MaxLogicalPathBytes       = 4096
	MaxObjectBytes      int64 = 100 * 1024 * 1024 * 1024
)

type Direction string

const (
	DirectionPut Direction = "put"
	DirectionGet Direction = "get"
)

type State string

const (
	StateReceiving            State = "receiving"
	StateValidating           State = "validating"
	StateCommitting           State = "committing"
	StatePublished            State = "published"
	StateAwaitingConfirmation State = "awaiting_confirmation"
	StateMaterializing        State = "materializing"
	StateVerifying            State = "verifying"
	StateLocal                State = "local"
	StateRejected             State = "rejected"
	StateCancelled            State = "cancelled"
)

// Identity is immutable for a generation_id. A retry presenting the same ID
// with any other repository, path, size or digest is a conflict, never a new
// upload accidentally appended to an old partial.
type Identity struct {
	LogicalRepoID string `json:"logical_repo_id"`
	LogicalPath   string `json:"logical_path"`
	GenerationID  string `json:"generation_id"`
	ExpectedSize  int64  `json:"expected_size"`
	SHA256        string `json:"sha256"`
}

func (i Identity) Validate() error {
	if err := validateCanonicalUUID("logical_repo_id", i.LogicalRepoID); err != nil {
		return err
	}
	if err := ValidateLogicalPath(i.LogicalPath); err != nil {
		return err
	}
	if err := validateCanonicalUUID("generation_id", i.GenerationID); err != nil {
		return err
	}
	if i.ExpectedSize < 1 || i.ExpectedSize > MaxObjectBytes {
		return fmt.Errorf("expected_size must be in range 1..%d", MaxObjectBytes)
	}
	if len(i.SHA256) != 64 || strings.ToLower(i.SHA256) != i.SHA256 {
		return errors.New("sha256 must be 64 lowercase hexadecimal characters")
	}
	for _, c := range i.SHA256 {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return errors.New("sha256 must be 64 lowercase hexadecimal characters")
		}
	}
	return nil
}

// StoragePath is the server-owned physical mapping. Clients and plugins only
// ever name LogicalPath; they cannot target the reserved SVN namespace.
func (i Identity) StoragePath() (string, error) {
	if err := i.Validate(); err != nil {
		return "", err
	}
	return ReservedNamespace + "/" + i.LogicalPath, nil
}

func ValidateLogicalPath(value string) error {
	if value == "" || len(value) > MaxLogicalPathBytes || !utf8.ValidString(value) {
		return errors.New("logical_path must be non-empty valid UTF-8 up to 4096 bytes")
	}
	if strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") || strings.Contains(value, "\\") || path.Clean(value) != value {
		return errors.New("logical_path must be a canonical repository-relative slash path")
	}
	for _, r := range value {
		if r == 0 || r < 0x20 || r == 0x7f {
			return errors.New("logical_path contains a control character")
		}
	}
	segments := strings.Split(value, "/")
	if segments[0] == ReservedNamespace {
		return errors.New("logical_path targets the reserved Whale namespace")
	}
	for _, segment := range segments {
		if segment == "" || segment == "." || segment == ".." {
			return errors.New("logical_path contains a non-canonical segment")
		}
	}
	return nil
}

// ValidateOffset codifies the resume invariant: OFFSET=N is the number of
// durable bytes and therefore also the zero-based index of the next byte.
func ValidateOffset(offset, expectedSize int64) error {
	if expectedSize < 1 || expectedSize > MaxObjectBytes {
		return errors.New("expected size is outside the Whale limit")
	}
	if offset < 0 || offset > expectedSize {
		return errors.New("offset must be in range 0..expected_size")
	}
	return nil
}

func (s State) Terminal() bool {
	return s == StatePublished || s == StateLocal || s == StateRejected || s == StateCancelled
}

func InitialState(direction Direction) (State, error) {
	switch direction {
	case DirectionPut:
		return StateReceiving, nil
	case DirectionGet:
		return StateAwaitingConfirmation, nil
	default:
		return "", errors.New("invalid Whale direction")
	}
}

func CanTransition(direction Direction, from, to State) bool {
	if from == to {
		return true
	}
	if from.Terminal() {
		return false
	}
	if to == StateRejected || to == StateCancelled {
		return true
	}
	if direction == DirectionPut {
		return (from == StateReceiving && to == StateValidating) ||
			(from == StateValidating && to == StateCommitting) ||
			(from == StateCommitting && to == StatePublished)
	}
	if direction == DirectionGet {
		return (from == StateAwaitingConfirmation && to == StateMaterializing) ||
			(from == StateMaterializing && to == StateVerifying) ||
			(from == StateVerifying && to == StateLocal)
	}
	return false
}

func validateCanonicalUUID(label, value string) error {
	id, err := uuid.Parse(value)
	if err != nil || id.String() != value {
		return fmt.Errorf("%s must be a canonical UUID", label)
	}
	return nil
}
