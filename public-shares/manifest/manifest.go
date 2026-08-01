// Package manifest defines the two channel declarations and their invariants.
//
// A share manifest declares what may be handed out; an upload manifest declares
// from whom something may be accepted and onto which shelf. They are separate
// types on purpose: the contracts have different errors, different states and
// different side effects, so a single manifest with a direction flag would glue
// two machines into one. See PUBLIC_SHARE_CONCEPT.md §3 and
// UPLOAD_CHANNEL_CONCEPT.md §3.
//
// Nothing here touches the filesystem or SVN. The public service consumes a
// declaration; it does not discover policy.
package manifest

import (
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"filees/public-shares/slug"
)

// CollisionPolicy decides what happens when an upload targets an existing name.
type CollisionPolicy string

const (
	// CollisionDeny is the default and the only policy implemented first.
	// Silent overwrite is never an option; see UPLOAD_CHANNEL_CONCEPT.md §3.4.
	CollisionDeny        CollisionPolicy = "deny"
	CollisionRename      CollisionPolicy = "rename"
	CollisionNewRevision CollisionPolicy = "new_revision"
)

func (p CollisionPolicy) valid() bool {
	switch p {
	case CollisionDeny, CollisionRename, CollisionNewRevision:
		return true
	}
	return false
}

// Object is one leaf a share hands out. The public identifier never carries the
// repository path: the recipient's request only ever names a public_id, and the
// mapping is resolved authoritatively (PUBLIC_SHARE_CONCEPT.md §3.2).
type Object struct {
	PublicID    string `json:"public_id"`
	RepoPath    string `json:"repo_path"`
	DisplayName string `json:"display_name"`
}

// Share is the declaration of intent to hand data out.
type Share struct {
	OwnerRealm string `json:"owner_realm"`
	RepoID     string `json:"repo_id"`
	SourceRoot string `json:"source_root"`
	Slug       string `json:"slug"`

	// Recipients, when non-empty, closes the channel: every recipient gets an
	// opaque token of their own. When empty the channel is open and Password
	// may gate it. A shared password on a closed channel is not a thing: it
	// spreads through informal chains and cannot be observed or withdrawn.
	Recipients []string `json:"recipients,omitempty"`
	Password   string   `json:"password_hash,omitempty"`

	// DoNotFollow, when present, freezes the channel at that revision. Absent
	// means the channel follows HEAD and the entry request freezes it per
	// visit. There is no mode field and no policy enum.
	DoNotFollow *int64 `json:"do-not-follow,omitempty"`

	Objects []Object `json:"object_map"`
}

// Upload is the declaration of intent to accept data.
type Upload struct {
	OwnerRealm string `json:"owner_realm"`

	// AuthorityRepoID is the project repository whose owner may open this
	// channel. It is never written to. UploadRepoID does not serve here: it
	// does not exist yet when CREATE is authorized, so it cannot be the source
	// of the right to create it (UPLOAD_CHANNEL_CONCEPT.md §2).
	AuthorityRepoID string `json:"authority_repo_id"`

	// UploadRepoID is the plain repository created for this channel — an
	// ordinary FileES repository the owner maps locally, the clean delivery
	// target for accepted uploads. It is never the project repository, and it
	// is not the realm-wide trash: rejected uploads go there instead, and
	// trash has no 1:1 relationship with a channel, so it is deliberately not
	// a field of this manifest (UPLOAD_CHANNEL_CONCEPT.md §2, §3, §3.2).
	UploadRepoID string `json:"upload_repo_id"`

	Slug string `json:"slug"`

	// Recipients is mandatory. An anonymous upload does not exist in this
	// model: there is no mode, configuration or exception that permits it.
	Recipients []string `json:"recipients"`

	// RequireOTP adds a one-time password and an explicit address on top of the
	// token. Available for any channel, at the owner's discretion.
	RequireOTP bool `json:"require_otp,omitempty"`

	CollisionPolicy CollisionPolicy `json:"collision_policy"`
}

var (
	ErrNoRecipients    = errors.New("upload channel requires a non-empty recipient list")
	ErrSameRepo        = errors.New("authority repository and upload repository must differ")
	ErrDuplicateObject = errors.New("duplicate public_id in object map")
	ErrClosedPassword  = errors.New("closed channel must not carry a shared password")
)

// Validate reports whether the share declaration is internally consistent.
func (s Share) Validate() error {
	if err := validUUID("owner_realm", s.OwnerRealm); err != nil {
		return err
	}
	if err := validUUID("repo_id", s.RepoID); err != nil {
		return err
	}
	if _, err := slug.Normalize(s.Slug); err != nil {
		return fmt.Errorf("slug: %w", err)
	}
	if err := validRepoPath("source_root", s.SourceRoot); err != nil {
		return err
	}
	if len(s.Recipients) > 0 && strings.TrimSpace(s.Password) != "" {
		// A closed channel authenticates by token. Keeping a shared password
		// alongside would reintroduce exactly the leak tokens remove.
		return ErrClosedPassword
	}
	for i, addr := range s.Recipients {
		if err := validRecipient(i, addr); err != nil {
			return err
		}
	}
	if s.DoNotFollow != nil && *s.DoNotFollow < 1 {
		// r0 is the empty repository root, so freezing a channel there
		// publishes nothing. Always a mistake, never an intent.
		return fmt.Errorf("do-not-follow: revision %d is not publishable", *s.DoNotFollow)
	}
	seen := make(map[string]struct{}, len(s.Objects))
	for i, obj := range s.Objects {
		if err := validPublicID(i, obj.PublicID); err != nil {
			return err
		}
		if _, dup := seen[obj.PublicID]; dup {
			return fmt.Errorf("%w: %s", ErrDuplicateObject, obj.PublicID)
		}
		seen[obj.PublicID] = struct{}{}
		if err := validRepoPath(fmt.Sprintf("object_map[%d].repo_path", i), obj.RepoPath); err != nil {
			return err
		}
		if err := validDisplayName(i, obj.DisplayName); err != nil {
			return err
		}
	}
	return nil
}

// Closed reports whether the share is gated by per-recipient tokens.
func (s Share) Closed() bool { return len(s.Recipients) > 0 }

// Frozen reports the pinned revision and whether one is declared. When it is
// not, the entry request freezes HEAD for the visit instead.
func (s Share) Frozen() (int64, bool) {
	if s.DoNotFollow == nil {
		return 0, false
	}
	return *s.DoNotFollow, true
}

// Validate reports whether the upload declaration is internally consistent.
func (u Upload) Validate() error {
	if err := validUUID("owner_realm", u.OwnerRealm); err != nil {
		return err
	}
	if err := validUUID("authority_repo_id", u.AuthorityRepoID); err != nil {
		return err
	}
	if err := validUUID("upload_repo_id", u.UploadRepoID); err != nil {
		return err
	}
	if u.AuthorityRepoID == u.UploadRepoID {
		return ErrSameRepo
	}
	if _, err := slug.Normalize(u.Slug); err != nil {
		return fmt.Errorf("slug: %w", err)
	}
	if len(u.Recipients) == 0 {
		return ErrNoRecipients
	}
	for i, addr := range u.Recipients {
		if err := validRecipient(i, addr); err != nil {
			return err
		}
	}
	if !u.CollisionPolicy.valid() {
		return fmt.Errorf("collision_policy: unsupported value %q", u.CollisionPolicy)
	}
	return nil
}

func validUUID(field, value string) error {
	if _, err := uuid.Parse(strings.TrimSpace(value)); err != nil {
		return fmt.Errorf("%s: not a UUID", field)
	}
	return nil
}

// validRepoPath accepts a repository-relative path and rejects anything that
// could climb out of it. The public side never supplies these, but a manifest
// is still data and gets the same treatment as any other input.
func validRepoPath(field, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("%s: empty", field)
	}
	if strings.HasPrefix(value, "/") {
		return fmt.Errorf("%s: must be repository-relative", field)
	}
	if strings.ContainsAny(value, "\\\x00") {
		return fmt.Errorf("%s: contains a forbidden character", field)
	}
	if strings.Contains(value, "//") {
		return fmt.Errorf("%s: contains an empty segment", field)
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "." || segment == ".." {
			return fmt.Errorf("%s: contains a relative segment", field)
		}
	}
	return nil
}

// validPublicID keeps the identifier opaque and safe as a single URL segment.
// It is generated server-side; the bound is a sanity check, not a parser.
func validPublicID(index int, value string) error {
	if len(value) < 16 || len(value) > 64 {
		return fmt.Errorf("object_map[%d].public_id: must contain 16 to 64 characters", index)
	}
	for i := 0; i < len(value); i++ {
		ch := value[i]
		if ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' {
			continue
		}
		return fmt.Errorf("object_map[%d].public_id: must be lowercase alphanumeric", index)
	}
	return nil
}

// validDisplayName guards the presentation string. Escaping happens at render
// time; here we only reject control characters, which have no business in a
// file name and break every listing format we might emit.
func validDisplayName(index int, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("object_map[%d].display_name: empty", index)
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("object_map[%d].display_name: contains a control character", index)
		}
	}
	return nil
}

// validRecipient checks the address shape only. The channel collects e-mail
// addresses and nothing else: there is no form for names, companies or
// anything further, and widening that would change what the system is.
func validRecipient(index int, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("recipients[%d]: empty", index)
	}
	at := strings.LastIndex(value, "@")
	if at <= 0 || at == len(value)-1 {
		return fmt.Errorf("recipients[%d]: not an e-mail address", index)
	}
	if strings.ContainsAny(value, " \t\r\n\x00,;") {
		return fmt.Errorf("recipients[%d]: contains a forbidden character", index)
	}
	return nil
}
