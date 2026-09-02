package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"filees/public-shares/channel"
)

// followsHead reports whether a channel is a live link rather than a release
// pinned to one revision. A pinned channel must never be advanced: its whole
// contract is that the revision does not move, so an unreachable pinned
// revision is a real error and not something to paper over with a refresh.
func followsHead(projection channel.Projection) bool { return projection.DoNotFollow == nil }

// refreshVisit issues a visit for the current HEAD of a live channel, keeping
// the subject the holder has already proved.
//
// A visit carries two different things and they age differently. The subject
// is the authorization, and it is re-validated against the live projection on
// every request by subjectStillAuthorized, so a changed password, a removed
// recipient or a revoked channel already invalidate it at once. The revision
// and frost proof are only a snapshot handle, and the proof covers a digest of
// the entire channel record - so renaming one file, which grants and revokes
// nothing, invalidates every outstanding visit on the object path while
// leaving the listing renderable. That is how a public link comes to show a
// correct listing where every file returns 404.
//
// For a link that follows HEAD the answer to a stale snapshot is a new
// snapshot, not a refusal. Re-entering grants no authority the holder did not
// already have: the subject is carried across unchanged and was checked before
// this is reached. It also makes revocation stricter rather than weaker,
// because the authority is consulted again instead of an old cookie being
// trusted.
//
// Returns false without an opinion when the channel is pinned, when the
// authority refuses, or when the record moved under us - callers fall back to
// what they would have done anyway.
func (h Handler) refreshVisit(ctx context.Context, alias, channelSlug string, claims visit) (channel.Projection, visit, string, bool) {
	entry, err := h.Backend.Enter(ctx, alias, channelSlug)
	if err != nil {
		return channel.Projection{}, visit{}, "", false
	}
	if !followsHead(entry.Projection) || entry.Projection.ChannelID != claims.ChannelID {
		return channel.Projection{}, visit{}, "", false
	}
	// Checked again against the projection Enter just returned: between the
	// caller's check and this call the channel may have been revoked, and a
	// refresh must not be the one path that misses it.
	if !subjectStillAuthorized(claims.Subject, entry.Projection) {
		return channel.Projection{}, visit{}, "", false
	}
	refreshed := claims
	refreshed.Revision = entry.Revision
	refreshed.FrostProof = entry.FrostProof
	// ExpiresAt is deliberately carried over rather than extended. A refresh
	// concerns the snapshot, not the session, and those are now different
	// things: the lifetime is what a visitor gets for entering a password or
	// exchanging an OTP once. Renewing it here would turn every listing render
	// into a session extension, so a gated share would stay open indefinitely
	// for anyone who keeps clicking - and the capability travels in the URL,
	// where it outlives the tab in history, bookmarks and pasted links.
	encoded, err := h.signVisit(refreshed)
	if err != nil {
		return channel.Projection{}, visit{}, "", false
	}
	return entry.Projection, refreshed, encoded, true
}

// withdrawnObjectNotice is what a visitor is told when the thing they clicked
// is no longer published. It names the event rather than the mechanism: they
// did not do anything wrong and cannot act on frost proofs or revisions.
const withdrawnObjectNotice = "Ten plik nie jest już częścią tego udostępnienia. Poniżej aktualna zawartość."

// explainWithdrawnObject answers a click on something the share no longer
// carries.
//
// Two situations arrive here and they deserve different answers. The visit may
// simply be holding an old snapshot, in which case a refresh finds the object
// and the caller can serve it. Or the object is genuinely withdrawn, and then a
// bare 404 is a poor answer: it leaves the visitor hunting for a file that was
// deliberately removed, with no way to tell that from a broken link. So the
// current listing is rendered with one sentence saying what happened.
//
// Falls back to the plain refusal when the channel is pinned or the authority
// will not answer, because inventing an explanation would be worse than none.
func (h Handler) explainWithdrawnObject(w http.ResponseWriter, request *http.Request, alias, channelSlug string, claims visit, publicID string) {
	freshProjection, freshClaims, freshEncoded, refreshed := h.refreshVisit(request.Context(), alias, channelSlug, claims)
	if !refreshed {
		h.notFound(w)
		return
	}
	if hasPublicObject(freshProjection, publicID) {
		// The snapshot was stale, not the share: send them back through the
		// normal path with a visit that can actually fetch it.
		h.redirectFile(w, request, alias, channelSlug, publicID, freshEncoded)
		return
	}
	h.renderListingNotice(w, freshProjection, freshClaims, freshEncoded, "", false, withdrawnObjectNotice)
}

// errVisitExpired marks a visit that verified correctly and simply ran out of
// time, so callers can tell it apart from one that failed to verify at all.
var errVisitExpired = errors.New("visit capability has expired")

// sessionDeadline renders how long a gated visitor keeps access, and stays
// silent otherwise.
//
// On an open link expiry falls through to a fresh entry, so nothing ends and
// announcing a deadline would be a threat the page will not carry out. Behind
// a password or an OTP the deadline is real, and it is the visitor's to see:
// they are the one who will lose a half-finished download to it.
//
// The figure is computed at render, which is honest without a ticking clock -
// the listing page runs under default-src 'none' with no script-src at all, so
// a live counter would mean opening script execution on the most exposed
// surface in the product. That is a decision of its own, not a detail of this
// one.
func (h Handler) sessionDeadline(claims visit) string {
	if claims.Subject == "" || claims.Subject == "open" || claims.ExpiresAt == 0 {
		return ""
	}
	remaining := time.Unix(claims.ExpiresAt, 0).Sub(h.now())
	if remaining <= 0 {
		return ""
	}
	minutes := int(remaining.Round(time.Minute) / time.Minute)
	if minutes < 1 {
		return "Dostęp wygasa lada chwila. Po wygaśnięciu trzeba będzie wpisać hasło ponownie."
	}
	return fmt.Sprintf("Dostęp wygasa za %d min. Potem trzeba będzie potwierdzić uprawnienie ponownie.", minutes)
}

// sessionCountdownCSS renders the depleting bar for a gated session and
// reports whether there is a bar to render.
//
// Everything the animation needs is known at render: the deadline is in the
// capability and the stylesheet is built per request and pinned by hash, so
// the duration and the starting fraction are baked in. Nothing recomputes
// client-side, which is the point - the page has no script-src and this must
// not become the reason to give it one.
//
// The bar is a second reading of the same fact the sentence already states, so
// it is aria-hidden: a screen reader gets the minutes, not a silently draining
// decoration.
func (h Handler) sessionCountdownCSS(claims visit) (string, bool) {
	if h.sessionDeadline(claims) == "" {
		return "", false
	}
	remaining := time.Unix(claims.ExpiresAt, 0).Sub(h.now())
	if remaining <= 0 {
		return "", false
	}
	fraction := float64(remaining) / float64(visitLifetime)
	if fraction > 1 {
		// A capability minted before the ceiling was lowered would otherwise
		// start the bar past full.
		fraction = 1
	}
	return fmt.Sprintf(
		".session-track{display:block;height:4px;margin-top:.5rem;border-radius:2px;background:rgba(11,29,58,.12);overflow:hidden}"+
			".session-fill{display:block;height:100%%;background:var(--owner-accent);transform-origin:left center;transform:scaleX(%.4f);animation:filees-session %ds linear forwards}"+
			"@keyframes filees-session{to{transform:scaleX(0)}}"+
			"@media (prefers-reduced-motion:reduce){.session-fill{animation:none}}",
		fraction, int(remaining.Seconds()),
	), true
}
