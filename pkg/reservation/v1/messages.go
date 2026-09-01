// Package v1 defines the transport-neutral wire protocol between a FileES
// desktop daemon and the remote FileES server's reservation-projection
// worker (cmd/filees-reservation-worker, invoked over the "filees
// reservation-v1" forced SSH command — see
// internal/servertool/client_entry.go and
// concepts/RESERVATION_SERVER_EMISSION_WORKPLAN.md).
//
// This is deliberately a separate, smaller protocol from pkg/control/v1:
// listing reservations is a read with no durable, retryable mutation
// semantics, so it does not need operation/request id tracking. Reusing
// pkg/control/v1's Ticket machinery for it would couple an unrelated read
// path to a much larger mutation-oriented contract for no benefit.
package v1

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
)

const Schema = "filees.reservation/v1"

// Reservation is one SVN lock as the server itself knows it — nothing more.
// Deliberately absent, and never to be added here:
//
//   - WorkingCopy: a client-local filesystem path. The server has no
//     concept of any client's own working copy layout.
//   - CanRelease / LocalChanges / ActivePassport: computed by comparing a
//     lock against one specific client's own passport manager state and
//     local file mtimes. Two different clients of the same realm can
//     legitimately compute different answers from the same lock — that
//     computation belongs entirely on the client side, as an overlay
//     applied after receiving a Result, never baked into the artifact.
//
// Comment is carried raw (not interpreted here) specifically so the client
// can run passport.ParseComment itself when building that overlay.
type Reservation struct {
	Path      string `json:"path"` // repository-relative, slash-separated
	Token     string `json:"token"`
	OwnerID   string `json:"owner_id,omitempty"`
	Comment   string `json:"comment,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
}

// Request asks the worker for the current reservation projection of one
// repository. The server derives which repository the authenticated client
// is even allowed to ask about from its own authz, exactly like every other
// control-plane request in this codebase — RepoID is a selector into that
// already-authorized set, not a capability grant by itself.
type Request struct {
	Schema string `json:"schema"`
	RepoID string `json:"repo_id"`
}

func (r Request) Validate() error {
	if r.Schema != Schema {
		return errors.New("reservation request schema mismatch")
	}
	if _, err := uuid.Parse(r.RepoID); err != nil {
		return errors.New("reservation request repo id must be a UUID")
	}
	return nil
}

// Result is the worker's answer. Exactly one of these is true, never a
// combination the caller has to guess between:
//
//   - fresh: Unknown=false, Stale=false — Reservations reflects a live
//     authority refresh that just succeeded.
//   - stale: Unknown=false, Stale=true — the live refresh failed, but a
//     prior confirmed artifact exists and is being replayed as-is.
//   - unknown: Unknown=true — the live refresh failed and no prior
//     artifact exists yet; Reservations is always empty and must never be
//     read as "confirmed zero".
type Result struct {
	Schema       string        `json:"schema"`
	RepoID       string        `json:"repo_id"`
	Reservations []Reservation `json:"reservations"`
	Stale        bool          `json:"stale"`
	Unknown      bool          `json:"unknown"`
	AsOf         time.Time     `json:"as_of,omitempty"`
	Generation   string        `json:"generation,omitempty"`
	// Detail carries the live-refresh failure's cause when Stale or Unknown
	// is set, so the daemon can log the real reason (see
	// concepts/RESERVATION_LISTING_RESILIENCE_CONCEPT.md §1.3a — the whole
	// point of this worker existing is that this must never again be
	// silently discarded).
	Detail string `json:"detail,omitempty"`
}

// decodeExactlyOne decodes exactly one JSON value from raw into v and
// rejects any further non-whitespace content after it — a second smuggled
// JSON value, or trailing garbage, must never be silently ignored on a
// protocol boundary.
func decodeExactlyOne(raw []byte, v any) error {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(v); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errors.New("reservation protocol: unexpected data after the JSON value")
	}
	return nil
}

func ParseRequest(raw []byte) (Request, error) {
	var req Request
	if err := decodeExactlyOne(raw, &req); err != nil {
		return Request{}, err
	}
	if err := req.Validate(); err != nil {
		return Request{}, err
	}
	return req, nil
}

func ParseResult(raw []byte) (Result, error) {
	var res Result
	if err := decodeExactlyOne(raw, &res); err != nil {
		return Result{}, err
	}
	if res.Schema != Schema || res.RepoID == "" {
		return Result{}, errors.New("reservation result schema or repo id missing")
	}
	if res.Unknown {
		if res.Stale || len(res.Reservations) != 0 {
			return Result{}, errors.New("reservation result unknown must carry no data and not also be stale")
		}
		if !res.AsOf.IsZero() || res.Generation != "" {
			return Result{}, errors.New("reservation result unknown must not carry as_of/generation")
		}
		return res, nil
	}
	// Fresh or stale: both describe an actually-confirmed artifact, which
	// Store.Refresh always stamps with a real timestamp and a generation
	// starting at 1 — a zero value here means the worker built this Result
	// wrong, not a legitimate state this protocol has a name for.
	if res.AsOf.IsZero() || res.Generation == "" {
		return Result{}, errors.New("reservation result fresh/stale must carry a non-zero as_of and generation")
	}
	return res, nil
}
