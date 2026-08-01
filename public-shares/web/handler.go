// Package web implements the stateless Public Shares HTTP surface. TLS and
// HTTP parsing belong to the fronting server; this handler is suitable for
// net/http/fcgi and never starts a listener itself.
package web

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"filees/public-shares/authority"
	"filees/public-shares/cache"
	"filees/public-shares/channel"
	"filees/public-shares/gate"
)

const visitLifetime = 12 * time.Hour

// A valid verifier may use up to 128 MiB. Public request concurrency must not
// become memory concurrency; rate limiting remains the fronting HTTP server's
// responsibility.
var passwordCheckSlot = make(chan struct{}, 1)

type Backend interface {
	Enter(context.Context, string, string) (authority.Entry, error)
	Inspect(string, string) (channel.Projection, error)
	Check(context.Context, authority.ObjectRequest) (authority.ObjectPermit, error)
	Fetch(context.Context, authority.ObjectRequest) (authority.FetchedLeaf, error)
}

type Handler struct {
	Backend  Backend
	Cache    *cache.Store
	Fetches  *FetchCoordinator
	VisitKey []byte
	Now      func() time.Time
}

type visit struct {
	Version    int    `json:"v"`
	ChannelID  string `json:"channel_id"`
	Revision   int64  `json:"revision"`
	FrostProof string `json:"frost_proof"`
	Subject    string `json:"subject"`
	ExpiresAt  int64  `json:"expires_at"`
}

func (h Handler) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	securityHeaders(w)
	if h.Backend == nil || len(h.VisitKey) < 32 {
		h.notFound(w)
		return
	}
	parts, ok := routeParts(request.URL)
	if !ok || (len(parts) != 2 && len(parts) != 4) {
		h.notFound(w)
		return
	}
	alias, channelSlug := parts[0], parts[1]
	if len(parts) == 2 {
		h.entry(w, request, alias, channelSlug)
		return
	}
	switch parts[2] {
	case "get":
		if request.Method != http.MethodGet {
			h.notFound(w)
			return
		}
		h.prepare(w, request, alias, channelSlug, parts[3])
	case "file":
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			h.notFound(w)
			return
		}
		h.file(w, request, alias, channelSlug, parts[3])
	default:
		h.notFound(w)
	}
}

func (h Handler) entry(w http.ResponseWriter, request *http.Request, alias, channelSlug string) {
	if request.Method != http.MethodGet && request.Method != http.MethodPost {
		h.notFound(w)
		return
	}
	if encoded := request.URL.Query().Get("v"); encoded != "" {
		projection, err := h.Backend.Inspect(alias, channelSlug)
		claims, err2 := h.verifyVisit(encoded, projection)
		if err != nil || err2 != nil {
			h.notFound(w)
			return
		}
		h.renderListing(w, projection, claims, encoded)
		return
	}
	entry, err := h.Backend.Enter(request.Context(), alias, channelSlug)
	if err != nil {
		h.notFound(w)
		return
	}
	token := request.URL.Query().Get("token")
	password := ""
	if request.Method == http.MethodPost {
		request.Body = http.MaxBytesReader(w, request.Body, 8*1024)
		if err := request.ParseForm(); err != nil {
			h.notFound(w)
			return
		}
		password = request.PostForm.Get("password")
	}
	if request.Method == http.MethodGet && entry.Projection.PasswordHash != "" && token == "" {
		h.renderPassword(w, alias, channelSlug)
		return
	}
	if entry.Projection.PasswordHash != "" {
		select {
		case passwordCheckSlot <- struct{}{}:
			defer func() { <-passwordCheckSlot }()
		default:
			h.notFound(w)
			return
		}
	}
	principal, err := gate.Authorize(entry.Projection, token, password)
	if err != nil {
		h.notFound(w)
		return
	}
	subject := policySubject(entry.Projection, principal, token)
	claims := visit{Version: 1, ChannelID: entry.Projection.ChannelID, Revision: entry.Revision, FrostProof: entry.FrostProof, Subject: subject, ExpiresAt: h.now().Add(visitLifetime).Unix()}
	encoded, err := h.signVisit(claims)
	if err != nil {
		h.notFound(w)
		return
	}
	http.Redirect(w, request, "/"+url.PathEscape(alias)+"/"+url.PathEscape(channelSlug)+"?v="+url.QueryEscape(encoded), http.StatusSeeOther)
}

func (h Handler) prepare(w http.ResponseWriter, request *http.Request, alias, channelSlug, publicID string) {
	projection, claims, encoded, ok := h.currentVisit(request, alias, channelSlug)
	if !ok || !hasPublicObject(projection, publicID) {
		h.notFound(w)
		return
	}
	objectRequest := authority.ObjectRequest{ChannelID: claims.ChannelID, PublicID: publicID, Revision: claims.Revision, FrostProof: claims.FrostProof}
	permit, err := h.Backend.Check(request.Context(), objectRequest)
	if err != nil {
		h.notFound(w)
		return
	}
	if h.Cache != nil {
		if hit, _, err := h.Cache.Open(permit.CacheKey, h.now()); err == nil {
			hit.Close()
			h.redirectFile(w, request, alias, channelSlug, publicID, encoded)
			return
		}
		if err := h.fillCache(request.Context(), objectRequest, permit.CacheKey); err != nil {
			h.notFound(w)
			return
		}
	}
	h.redirectFile(w, request, alias, channelSlug, publicID, encoded)
}

func (h Handler) file(w http.ResponseWriter, request *http.Request, alias, channelSlug, publicID string) {
	projection, claims, _, ok := h.currentVisit(request, alias, channelSlug)
	if !ok || !hasPublicObject(projection, publicID) {
		h.notFound(w)
		return
	}
	objectRequest := authority.ObjectRequest{ChannelID: claims.ChannelID, PublicID: publicID, Revision: claims.Revision, FrostProof: claims.FrostProof}
	permit, err := h.Backend.Check(request.Context(), objectRequest)
	if err != nil {
		h.notFound(w)
		return
	}
	name := permit.DisplayName
	if h.Cache != nil {
		file, _, err := h.Cache.Open(permit.CacheKey, h.now())
		if err != nil {
			if fillErr := h.fillCache(request.Context(), objectRequest, permit.CacheKey); fillErr != nil {
				h.notFound(w)
				return
			}
			file, _, err = h.Cache.Open(permit.CacheKey, h.now())
		}
		if err != nil {
			h.notFound(w)
			return
		}
		defer file.Close()
		h.serveAttachment(w, request, name, file)
		return
	}
	leaf, err := h.Backend.Fetch(request.Context(), objectRequest)
	if err != nil {
		h.notFound(w)
		return
	}
	defer leaf.Body.Close()
	if seeker, ok := leaf.Body.(io.ReadSeeker); ok {
		h.serveAttachment(w, request, name, seeker)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", contentDisposition(name))
	w.Header().Set("Content-Length", strconv.FormatInt(leaf.Size, 10))
	if request.Method != http.MethodHead {
		_, _ = io.Copy(w, leaf.Body)
	}
}

func (h Handler) fillCache(ctx context.Context, request authority.ObjectRequest, key string) error {
	fill := func() error {
		if hit, _, err := h.Cache.Open(key, h.now()); err == nil {
			return hit.Close()
		}
		leaf, err := h.Backend.Fetch(ctx, request)
		if err != nil {
			return err
		}
		defer leaf.Body.Close()
		if leaf.CacheKey != key {
			return errors.New("public share fetch changed cache key")
		}
		return h.Cache.Put(leaf.CacheKey, leaf.Body, leaf.Size, leaf.MD5, h.now())
	}
	if h.Fetches == nil {
		return fill()
	}
	return h.Fetches.Do(ctx, key, fill)
}

// FetchCoordinator coalesces concurrent misses for one opaque cache key. It
// carries no policy and no bytes; waiters receive only the leader's result.
type FetchCoordinator struct {
	mu      sync.Mutex
	pending map[string]*fetchCall
}

type fetchCall struct {
	done chan struct{}
	err  error
}

func (c *FetchCoordinator) Do(ctx context.Context, key string, fn func() error) error {
	c.mu.Lock()
	if c.pending == nil {
		c.pending = map[string]*fetchCall{}
	}
	if call, ok := c.pending[key]; ok {
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-call.done:
			return call.err
		}
	}
	call := &fetchCall{done: make(chan struct{})}
	c.pending[key] = call
	c.mu.Unlock()

	call.err = fn()
	c.mu.Lock()
	delete(c.pending, key)
	close(call.done)
	c.mu.Unlock()
	return call.err
}

func (h Handler) currentVisit(request *http.Request, alias, channelSlug string) (channel.Projection, visit, string, bool) {
	encoded := request.URL.Query().Get("v")
	projection, err := h.Backend.Inspect(alias, channelSlug)
	if err != nil {
		return channel.Projection{}, visit{}, "", false
	}
	claims, err := h.verifyVisit(encoded, projection)
	return projection, claims, encoded, err == nil
}

func (h Handler) signVisit(claims visit) (string, error) {
	raw, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(raw)
	mac := hmac.New(sha256.New, h.VisitKey)
	_, _ = io.WriteString(mac, payload)
	return payload + "." + hex.EncodeToString(mac.Sum(nil)), nil
}

func (h Handler) verifyVisit(encoded string, projection channel.Projection) (visit, error) {
	parts := strings.Split(encoded, ".")
	if len(parts) != 2 || len(parts[1]) != sha256.Size*2 {
		return visit{}, errors.New("visit capability is malformed")
	}
	want, err := hex.DecodeString(parts[1])
	if err != nil {
		return visit{}, err
	}
	mac := hmac.New(sha256.New, h.VisitKey)
	_, _ = io.WriteString(mac, parts[0])
	if !hmac.Equal(want, mac.Sum(nil)) {
		return visit{}, errors.New("visit capability signature is invalid")
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(parts[0])
	if err != nil {
		return visit{}, err
	}
	var claims visit
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&claims); err != nil {
		return visit{}, errors.New("visit capability claims are invalid")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) || claims.Version != 1 || claims.ChannelID != projection.ChannelID || claims.Revision < 1 || claims.FrostProof == "" || h.now().Unix() >= claims.ExpiresAt {
		return visit{}, errors.New("visit capability claims are invalid")
	}
	if !subjectStillAuthorized(claims.Subject, projection) {
		return visit{}, errors.New("visit capability policy is no longer active")
	}
	return claims, nil
}

func policySubject(projection channel.Projection, principal gate.Principal, token string) string {
	if len(projection.Recipients) > 0 {
		return "recipient:" + gate.TokenHash(token)
	}
	if projection.PasswordHash != "" {
		digest := sha256.Sum256([]byte(projection.PasswordHash))
		return "password:" + hex.EncodeToString(digest[:])
	}
	return "open"
}

func subjectStillAuthorized(subject string, projection channel.Projection) bool {
	switch {
	case subject == "open":
		return len(projection.Recipients) == 0 && projection.PasswordHash == ""
	case strings.HasPrefix(subject, "password:"):
		digest := sha256.Sum256([]byte(projection.PasswordHash))
		return projection.PasswordHash != "" && hmac.Equal([]byte(strings.TrimPrefix(subject, "password:")), []byte(hex.EncodeToString(digest[:])))
	case strings.HasPrefix(subject, "recipient:"):
		want := strings.TrimPrefix(subject, "recipient:")
		matched := 0
		for _, recipient := range projection.Recipients {
			matched |= subtleEqual(want, recipient.TokenHash)
		}
		return matched == 1
	}
	return false
}

func subtleEqual(a, b string) int {
	if len(a) != len(b) {
		return 0
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b))
}

func hasPublicObject(projection channel.Projection, publicID string) bool {
	for _, object := range projection.Objects {
		if object.PublicID == publicID {
			return true
		}
	}
	return false
}

func (h Handler) renderListing(w http.ResponseWriter, projection channel.Projection, claims visit, encoded string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	data := struct {
		Alias, Slug, Visit string
		Revision           int64
		Objects            []channel.PublicObject
	}{projection.Alias, projection.Slug, encoded, claims.Revision, projection.Objects}
	_ = listingTemplate.Execute(w, data)
}

func (h Handler) renderPassword(w http.ResponseWriter, alias, channelSlug string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = passwordTemplate.Execute(w, struct{ Action string }{Action: "/" + url.PathEscape(alias) + "/" + url.PathEscape(channelSlug)})
}

func (h Handler) serveAttachment(w http.ResponseWriter, request *http.Request, name string, content io.ReadSeeker) {
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", contentDisposition(name))
	w.Header().Set("Cache-Control", "private, no-store")
	http.ServeContent(w, request, name, time.Time{}, content)
}

func contentDisposition(name string) string {
	value := mime.FormatMediaType("attachment", map[string]string{"filename": name})
	if value == "" {
		return "attachment"
	}
	return value
}

func (h Handler) redirectFile(w http.ResponseWriter, request *http.Request, alias, channelSlug, publicID, encoded string) {
	target := fmt.Sprintf("/%s/%s/file/%s?v=%s", url.PathEscape(alias), url.PathEscape(channelSlug), url.PathEscape(publicID), url.QueryEscape(encoded))
	http.Redirect(w, request, target, http.StatusSeeOther)
}

func routeParts(u *url.URL) ([]string, bool) {
	escaped := strings.Trim(u.EscapedPath(), "/")
	if escaped == "" {
		return nil, false
	}
	raw := strings.Split(escaped, "/")
	parts := make([]string, len(raw))
	for i, part := range raw {
		decoded, err := url.PathUnescape(part)
		if err != nil || decoded == "" || strings.ContainsAny(decoded, "/\\\x00") {
			return nil, false
		}
		parts[i] = decoded
	}
	return parts, true
}

func securityHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy", "default-src 'none'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
}

func (h Handler) notFound(w http.ResponseWriter) { http.Error(w, "not found", http.StatusNotFound) }
func (h Handler) now() time.Time {
	if h.Now != nil {
		return h.Now().UTC()
	}
	return time.Now().UTC()
}

var listingTemplate = template.Must(template.New("listing").Parse(`<!doctype html><html lang="pl"><meta charset="utf-8"><title>Pliki do pobrania</title><h1>Pliki do pobrania</h1><p>Wydanie r{{.Revision}}</p><ul>{{range .Objects}}<li><a rel="nofollow" href="/{{$.Alias}}/{{$.Slug}}/get/{{.PublicID}}?v={{$.Visit}}">{{.DisplayName}}</a></li>{{end}}</ul></html>`))
var passwordTemplate = template.Must(template.New("password").Parse(`<!doctype html><html lang="pl"><meta charset="utf-8"><title>Dostęp do plików</title><form method="post" action="{{.Action}}"><label>Hasło <input type="password" name="password" autocomplete="current-password" required></label><button type="submit">Otwórz</button></form></html>`))
