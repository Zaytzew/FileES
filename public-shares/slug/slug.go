// Package slug implements the public addressing namespace for share and upload
// channels: /<realm-alias>/<slug>.
//
// The realm-alias prefix is not decoration. Without it the slug space would be
// global, which yields both squatting and a cross-realm leak: a SLUG_TAKEN
// answer would let anyone enumerate what other realms have published, and
// tender names are not hard to guess. See PUBLIC_SHARE_CONCEPT.md §5.1.
//
// Alias normalization itself belongs to pkg/realmalias and is not repeated
// here; this package only validates the slug component and composes the path.
package slug

import (
	"errors"
	"strings"
)

const (
	// MinLen and MaxLen bound the slug component. Slugs are chosen by humans
	// and pasted into mail, so they stay short enough to survive that.
	MinLen = 3
	MaxLen = 64
)

// reserved holds slug values that would collide with routing below the channel
// root (/<alias>/<slug>/get/... and /file/...) or read as infrastructure.
// Rejecting them at creation is cheaper than discovering the ambiguity later.
var reserved = map[string]struct{}{
	"get": {}, "file": {}, "up": {}, "api": {}, "static": {}, "assets": {},
	"health": {}, "status": {}, "robots.txt": {}, "favicon.ico": {},
	"admin": {}, "internal": {}, "public": {},
}

var (
	ErrEmpty      = errors.New("slug is empty")
	ErrLength     = errors.New("slug must contain 3 to 64 characters")
	ErrEdge       = errors.New("slug must start and end with a letter or digit")
	ErrCharset    = errors.New("slug may contain only lowercase letters, digits and '-'")
	ErrDoubleDash = errors.New("slug may not contain consecutive hyphens")
	ErrReserved   = errors.New("slug is reserved")
)

// Normalize lowercases and trims value, then validates it as a slug.
//
// The rule is deliberately narrower than pkg/realmalias.Normalize: no dots and
// no underscores. A slug travels in mail bodies and gets read aloud over the
// phone, and '.' in a path segment invites confusion with file extensions.
func Normalize(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "", ErrEmpty
	}
	if len(value) < MinLen || len(value) > MaxLen {
		return "", ErrLength
	}
	if !letterOrDigit(value[0]) || !letterOrDigit(value[len(value)-1]) {
		return "", ErrEdge
	}
	for i := 0; i < len(value); i++ {
		ch := value[i]
		if letterOrDigit(ch) {
			continue
		}
		if ch != '-' {
			return "", ErrCharset
		}
		if i > 0 && value[i-1] == '-' {
			return "", ErrDoubleDash
		}
	}
	if _, bad := reserved[value]; bad {
		return "", ErrReserved
	}
	return value, nil
}

// Path composes the public path for a channel. Both components must already be
// normalized; callers get an error rather than a silently malformed path.
func Path(alias, channelSlug string) (string, error) {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return "", errors.New("realm alias is empty")
	}
	if strings.ContainsAny(alias, "/\\") {
		return "", errors.New("realm alias must be a single path segment")
	}
	normalized, err := Normalize(channelSlug)
	if err != nil {
		return "", err
	}
	return "/" + alias + "/" + normalized, nil
}

func letterOrDigit(ch byte) bool {
	return ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9'
}
