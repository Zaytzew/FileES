// Package web implements the stateless Public Shares HTTP surface. TLS and
// HTTP parsing belong to the fronting server; this handler is suitable for
// net/http/fcgi and never starts a listener itself.
package web

import (
	"archive/zip"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
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
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"filees/pkg/realmbranding"
	"filees/public-shares/authority"
	"filees/public-shares/cache"
	"filees/public-shares/channel"
	"filees/public-shares/gate"
	"filees/public-shares/intake"
	"filees/public-shares/recipientotp"
)

const visitLifetime = 12 * time.Hour

// A valid verifier may use up to 128 MiB. Public request concurrency must not
// become memory concurrency; rate limiting remains the fronting HTTP server's
// responsibility.
var passwordCheckSlot = make(chan struct{}, 1)

type Backend interface {
	Enter(context.Context, string, string) (authority.Entry, error)
	Inspect(string, string) (channel.Projection, error)
	InspectUpload(string, string) (channel.UploadProjection, error)
	Check(context.Context, authority.ObjectRequest) (authority.ObjectPermit, error)
	Fetch(context.Context, authority.ObjectRequest) (authority.FetchedLeaf, error)
	RequestRecipientOTP(context.Context, recipientotp.Request) error
	VerifyRecipientOTP(context.Context, recipientotp.VerifyRequest) (recipientotp.Grant, error)
}

type Handler struct {
	Backend        Backend
	Cache          *cache.Store
	Fetches        *FetchCoordinator
	VisitKey       []byte
	MaxBundleFiles int
	MaxBundleSize  int64
	BundleSlots    chan struct{}
	Intake         *intake.Store
	MaxUploadBytes int64
	Now            func() time.Time
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
	if !ok || (len(parts) != 2 && len(parts) != 3 && len(parts) != 4) {
		h.notFound(w)
		return
	}
	alias, channelSlug := parts[0], parts[1]
	if len(parts) == 2 {
		h.entry(w, request, alias, channelSlug)
		return
	}
	if len(parts) == 3 {
		if parts[2] != "bundle" || request.Method != http.MethodPost {
			h.notFound(w)
			return
		}
		h.bundle(w, request, alias, channelSlug)
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
	invitation := request.URL.Query().Get("invite")
	if invitation == "" {
		invitation = request.URL.Query().Get("token") // links sent before recipient OTP shipped
	}
	if encoded := request.URL.Query().Get("v"); encoded != "" {
		projection, err := h.Backend.Inspect(alias, channelSlug)
		claims, err2 := h.verifyVisit(encoded, projection)
		if err != nil || err2 != nil {
			if err == nil && len(projection.Recipients) > 0 && invitation != "" {
				h.renderRecipient(w, projection, invitation, false, false)
				return
			}
			h.notFound(w)
			return
		}
		projection, err = h.inspectAt(request.Context(), alias, channelSlug, claims.Revision, projection)
		if err != nil || projection.ChannelID != claims.ChannelID || !subjectStillAuthorized(claims.Subject, projection) {
			h.notFound(w)
			return
		}
		h.renderListingWithInvitation(w, projection, claims, encoded, invitation, request.URL.Query().Get("notice") == "select")
		return
	}
	entry, err := h.Backend.Enter(request.Context(), alias, channelSlug)
	if err != nil {
		h.uploadEntry(w, request, alias, channelSlug)
		return
	}
	if len(entry.Projection.Recipients) > 0 {
		h.recipientEntry(w, request, entry, invitation)
		return
	}
	password := ""
	if request.Method == http.MethodPost {
		request.Body = http.MaxBytesReader(w, request.Body, 8*1024)
		if err := request.ParseForm(); err != nil {
			h.notFound(w)
			return
		}
		password = request.PostForm.Get("password")
	}
	if request.Method == http.MethodGet && entry.Projection.PasswordHash != "" {
		h.renderPassword(w, entry.Projection)
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
	principal, err := gate.Authorize(entry.Projection, "", password)
	if err != nil {
		h.notFound(w)
		return
	}
	subject := policySubject(entry.Projection, principal)
	claims := visit{Version: 1, ChannelID: entry.Projection.ChannelID, Revision: entry.Revision, FrostProof: entry.FrostProof, Subject: subject, ExpiresAt: h.now().Add(visitLifetime).Unix()}
	encoded, err := h.signVisit(claims)
	if err != nil {
		h.notFound(w)
		return
	}
	http.Redirect(w, request, "/"+url.PathEscape(alias)+"/"+url.PathEscape(channelSlug)+"?v="+url.QueryEscape(encoded), http.StatusSeeOther)
}

func (h Handler) recipientEntry(w http.ResponseWriter, request *http.Request, entry authority.Entry, invitation string) {
	if invitation == "" {
		h.notFound(w)
		return
	}
	if request.Method == http.MethodGet {
		h.renderRecipient(w, entry.Projection, invitation, false, false)
		return
	}
	request.Body = http.MaxBytesReader(w, request.Body, 8*1024)
	if err := request.ParseForm(); err != nil {
		h.notFound(w)
		return
	}
	action := request.PostForm.Get("action")
	base := recipientotp.Request{Alias: entry.Projection.Alias, Slug: entry.Projection.Slug, Invitation: invitation}
	if action == "send" {
		// The same response is rendered for valid and invalid invitations so the
		// public surface never confirms that a mailbox is on the ACL.
		_ = h.Backend.RequestRecipientOTP(request.Context(), base)
		h.renderRecipient(w, entry.Projection, invitation, true, false)
		return
	}
	if action != "verify" {
		h.notFound(w)
		return
	}
	grant, err := h.Backend.VerifyRecipientOTP(request.Context(), recipientotp.VerifyRequest{Request: base, Code: request.PostForm.Get("code")})
	digest := gate.TokenHash(invitation)
	now := h.now()
	if err != nil || !hmac.Equal([]byte(grant.InvitationHash), []byte(digest)) || grant.Epoch == "" || !grant.ExpiresAt.After(now) || grant.ExpiresAt.After(now.Add(recipientotp.DefaultTTL)) {
		h.renderRecipient(w, entry.Projection, invitation, true, true)
		return
	}
	claims := visit{
		Version: 1, ChannelID: entry.Projection.ChannelID, Revision: entry.Revision,
		FrostProof: entry.FrostProof, Subject: "recipient:" + digest + ":" + grant.Epoch,
		ExpiresAt: grant.ExpiresAt.Unix(),
	}
	encoded, err := h.signVisit(claims)
	if err != nil {
		h.notFound(w)
		return
	}
	target := fmt.Sprintf("/%s/%s?invite=%s&v=%s", url.PathEscape(entry.Projection.Alias), url.PathEscape(entry.Projection.Slug), url.QueryEscape(invitation), url.QueryEscape(encoded))
	http.Redirect(w, request, target, http.StatusSeeOther)
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

type bundleLeaf struct {
	cacheKey string
	name     string
	size     int64
}

func (h Handler) bundle(w http.ResponseWriter, request *http.Request, alias, channelSlug string) {
	if h.Cache == nil || h.MaxBundleFiles < 1 || h.MaxBundleSize < 1 {
		h.notFound(w)
		return
	}
	request.Body = http.MaxBytesReader(w, request.Body, 512<<10)
	if err := request.ParseForm(); err != nil {
		h.notFound(w)
		return
	}
	projection, claims, encoded, ok := h.currentVisit(request, alias, channelSlug)
	if !ok {
		h.notFound(w)
		return
	}
	if h.BundleSlots != nil {
		select {
		case h.BundleSlots <- struct{}{}:
			defer func() { <-h.BundleSlots }()
		default:
			h.notFound(w)
			return
		}
	}
	objects, archiveName, selected := selectBundleObjects(projection, request.PostForm)
	if !selected {
		target := visitURL(fmt.Sprintf("/%s/%s", url.PathEscape(alias), url.PathEscape(channelSlug)), encoded, request.URL.Query().Get("invite")) + "&notice=select"
		http.Redirect(w, request, target, http.StatusSeeOther)
		return
	}
	if len(objects) < 1 || len(objects) > h.MaxBundleFiles {
		h.notFound(w)
		return
	}
	leaves, err := h.prepareBundle(request.Context(), claims, objects)
	if err != nil {
		h.notFound(w)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", contentDisposition(archiveName+".zip"))
	w.Header().Set("Cache-Control", "private, no-store")
	archive := zip.NewWriter(w)
	for _, leaf := range leaves {
		file, _, openErr := h.Cache.Open(leaf.cacheKey, h.now())
		if openErr != nil {
			_ = archive.Close()
			return
		}
		header := &zip.FileHeader{Name: leaf.name, Method: zip.Store}
		header.SetModTime(time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC))
		entry, createErr := archive.CreateHeader(header)
		if createErr == nil {
			_, createErr = io.CopyN(entry, file, leaf.size)
		}
		_ = file.Close()
		if createErr != nil {
			_ = archive.Close()
			return
		}
	}
	_ = archive.Close()
}

func selectBundleObjects(projection channel.Projection, form url.Values) ([]channel.PublicObject, string, bool) {
	if form.Get("all") == "1" {
		return append([]channel.PublicObject(nil), projection.Objects...), safeArchivePart(projection.Slug), true
	}
	if folder := form.Get("folder"); folder != "" {
		folder = strings.Join(listingPathParts(folder), "/")
		prefix := folder + "/"
		objects := make([]channel.PublicObject, 0)
		for _, object := range projection.Objects {
			if strings.HasPrefix(objectDisplayPath(object), prefix) {
				objects = append(objects, object)
			}
		}
		parts := listingPathParts(folder)
		return objects, safeArchivePart(parts[len(parts)-1]), len(objects) > 0
	}
	wanted := make(map[string]bool)
	for _, publicID := range form["object"] {
		if publicID != "" {
			wanted[publicID] = true
		}
	}
	objects := make([]channel.PublicObject, 0, len(wanted))
	for _, object := range projection.Objects {
		if wanted[object.PublicID] {
			objects = append(objects, object)
			delete(wanted, object.PublicID)
		}
	}
	if len(objects) == 0 || len(wanted) != 0 {
		return nil, "", false
	}
	return objects, safeArchivePart(projection.Slug) + "-wybrane", true
}

func (h Handler) prepareBundle(ctx context.Context, claims visit, objects []channel.PublicObject) ([]bundleLeaf, error) {
	leaves := make([]bundleLeaf, 0, len(objects))
	var total int64
	usedNames := make(map[string]int)
	for _, object := range objects {
		objectRequest := authority.ObjectRequest{ChannelID: claims.ChannelID, PublicID: object.PublicID, Revision: claims.Revision, FrostProof: claims.FrostProof}
		permit, err := h.Backend.Check(ctx, objectRequest)
		if err != nil {
			return nil, err
		}
		file, size, err := h.Cache.Open(permit.CacheKey, h.now())
		if err != nil {
			if err = h.fillCache(ctx, objectRequest, permit.CacheKey); err != nil {
				return nil, err
			}
			file, size, err = h.Cache.Open(permit.CacheKey, h.now())
		}
		if err != nil {
			return nil, err
		}
		_ = file.Close()
		if size < 0 || total > h.MaxBundleSize-size {
			return nil, errors.New("public share bundle exceeds size limit")
		}
		total += size
		name := uniqueArchiveName(safeArchivePath(object.DisplayName), usedNames)
		leaves = append(leaves, bundleLeaf{cacheKey: permit.CacheKey, name: name, size: size})
	}
	return leaves, nil
}

func objectDisplayPath(object channel.PublicObject) string {
	return strings.Join(listingPathParts(object.DisplayName), "/")
}

func safeArchivePath(displayName string) string {
	parts := listingPathParts(displayName)
	for index := range parts {
		parts[index] = safeArchivePart(parts[index])
	}
	return strings.Join(parts, "/")
}

func safeArchivePart(value string) string {
	value = strings.Map(func(character rune) rune {
		if character < 32 || character == 127 || strings.ContainsRune("\\/:*?\"<>|", character) {
			return '_'
		}
		return character
	}, value)
	value = strings.TrimSpace(value)
	value = strings.Trim(value, ".")
	if value == "" {
		return "plik"
	}
	return value
}

func uniqueArchiveName(name string, used map[string]int) string {
	key := strings.ToLower(name)
	used[key]++
	if used[key] == 1 {
		return name
	}
	extension := path.Ext(name)
	base := strings.TrimSuffix(name, extension)
	for suffix := used[key]; ; suffix++ {
		candidate := fmt.Sprintf("%s (%d)%s", base, suffix, extension)
		candidateKey := strings.ToLower(candidate)
		if used[candidateKey] == 0 {
			used[candidateKey] = 1
			return candidate
		}
	}
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
	if err != nil {
		return channel.Projection{}, visit{}, "", false
	}
	projection, err = h.inspectAt(request.Context(), alias, channelSlug, claims.Revision, projection)
	if err != nil || projection.ChannelID != claims.ChannelID || !subjectStillAuthorized(claims.Subject, projection) {
		return channel.Projection{}, visit{}, "", false
	}
	return projection, claims, encoded, true
}

func (h Handler) inspectAt(ctx context.Context, alias, channelSlug string, revision int64, fallback channel.Projection) (channel.Projection, error) {
	inspector, ok := h.Backend.(interface {
		InspectAt(context.Context, string, string, int64) (channel.Projection, error)
	})
	if !ok {
		return fallback, nil
	}
	return inspector.InspectAt(ctx, alias, channelSlug, revision)
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

func policySubject(projection channel.Projection, _ gate.Principal) string {
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
		parts := strings.Split(strings.TrimPrefix(subject, "recipient:"), ":")
		if len(parts) != 2 || parts[1] == "" {
			return false
		}
		want := parts[0]
		matched := 0
		for _, recipient := range projection.Recipients {
			matched |= subtleEqual(want, recipient.InvitationHash)
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

func (h Handler) renderListing(w http.ResponseWriter, projection channel.Projection, claims visit, encoded string, selectionNotice bool) {
	h.renderListingWithInvitation(w, projection, claims, encoded, "", selectionNotice)
}

func (h Handler) renderListingWithInvitation(w http.ResponseWriter, projection channel.Projection, claims visit, encoded, invitation string, selectionNotice bool) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	branding, err := realmbranding.Normalize(projection.Branding)
	if err != nil {
		branding = realmbranding.Default()
	}
	ownerInk := "#0B1D3A"
	if branding.LeadingColor != realmbranding.DefaultLeadingColor {
		ownerInk = branding.LeadingColor
	}
	css := listingCSS + listingCSSOverrides + brandCSSOverrides + iconColorCSS + ":root{--owner-accent:" + branding.LeadingColor + ";--owner-ink:" + ownerInk + "}"
	digest := sha256.Sum256([]byte(css))
	cssHash := base64.StdEncoding.EncodeToString(digest[:])
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'sha256-"+cssHash+"'; img-src data:; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
	bundles := h.Cache != nil && h.MaxBundleFiles > 0 && h.MaxBundleSize > 0
	data := listingPage{
		Alias:           projection.Alias,
		Slug:            projection.Slug,
		Revision:        claims.Revision,
		LatestURL:       latestURL(projection.Alias, projection.Slug, invitation),
		Count:           len(projection.Objects),
		CountText:       formatListingCount(len(projection.Objects)),
		Tree:            buildListingTree(projection, encoded, bundles),
		Bundles:         bundles,
		BundleURL:       visitURL(fmt.Sprintf("/%s/%s/bundle", url.PathEscape(projection.Alias), url.PathEscape(projection.Slug)), encoded, invitation),
		SelectionNotice: selectionNotice,
		BrandSymbol:     brandSymbol,
		DownloadIcon:    listingIcon("download"),
		ArchiveIcon:     listingIcon("archive"),
		CSS:             template.CSS(css),
	}
	if branding.LogoBase64 != "" {
		data.HasOwnerLogo = true
		data.OwnerLogo = template.URL("data:" + branding.LogoMediaType + ";base64," + branding.LogoBase64)
	}
	_ = listingTemplate.Execute(w, data)
}

type listingPage struct {
	Alias, Slug                            string
	Revision                               int64
	Count                                  int
	CountText, BundleURL, LatestURL        string
	Tree                                   listingDirectory
	Bundles, SelectionNotice               bool
	BrandSymbol, DownloadIcon, ArchiveIcon template.HTML
	CSS                                    template.CSS
	OwnerLogo                              template.URL
	HasOwnerLogo                           bool
}

type listingDirectory struct {
	Name, Path               string
	Directories              []listingDirectory
	Files                    []listingFile
	BundleEnabled            bool
	FolderIcon, DownloadIcon template.HTML
}

type listingFile struct {
	Name, Type, URL, Size, PublicID, IconClass string
	SizeKnown, BundleEnabled                   bool
	Icon                                       template.HTML
}

type listingTreeBuilder struct {
	name, path  string
	directories map[string]*listingTreeBuilder
	files       []listingFile
}

func buildListingTree(projection channel.Projection, visit string, bundles bool) listingDirectory {
	root := &listingTreeBuilder{directories: map[string]*listingTreeBuilder{}}
	for _, object := range projection.Objects {
		parts := listingPathParts(object.DisplayName)
		directory := root
		for index, part := range parts[:len(parts)-1] {
			child := directory.directories[part]
			if child == nil {
				child = &listingTreeBuilder{name: part, path: strings.Join(parts[:index+1], "/"), directories: map[string]*listingTreeBuilder{}}
				directory.directories[part] = child
			}
			directory = child
		}
		typeName, iconName := listingFileType(parts[len(parts)-1])
		file := listingFile{
			Name:          parts[len(parts)-1],
			Type:          typeName,
			Icon:          listingIcon(iconName),
			IconClass:     iconName,
			PublicID:      object.PublicID,
			BundleEnabled: bundles,
			URL:           fmt.Sprintf("/%s/%s/get/%s?v=%s", url.PathEscape(projection.Alias), url.PathEscape(projection.Slug), url.PathEscape(object.PublicID), url.QueryEscape(visit)),
		}
		if object.Size != nil {
			file.SizeKnown = true
			file.Size = formatListingSize(*object.Size)
		}
		directory.files = append(directory.files, file)
	}
	return freezeListingTree(root, bundles)
}

func listingPathParts(displayName string) []string {
	parts := strings.Split(displayName, "/")
	clean := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" && part != "." && part != ".." {
			clean = append(clean, part)
		}
	}
	if len(clean) == 0 {
		return []string{displayName}
	}
	return clean
}

func freezeListingTree(node *listingTreeBuilder, bundles bool) listingDirectory {
	result := listingDirectory{Name: node.name, Path: node.path, Files: append([]listingFile(nil), node.files...), BundleEnabled: bundles, FolderIcon: listingIcon("folder"), DownloadIcon: listingIcon("download")}
	for _, child := range node.directories {
		result.Directories = append(result.Directories, freezeListingTree(child, bundles))
	}
	sort.SliceStable(result.Directories, func(i, j int) bool { return listingLess(result.Directories[i].Name, result.Directories[j].Name) })
	sort.SliceStable(result.Files, func(i, j int) bool { return listingLess(result.Files[i].Name, result.Files[j].Name) })
	return result
}

func listingLess(a, b string) bool {
	aFold, bFold := strings.ToLower(a), strings.ToLower(b)
	if compared := naturalListingCompare(aFold, bFold); compared != 0 {
		return compared < 0
	}
	return a < b
}

func naturalListingCompare(a, b string) int {
	for i, j := 0, 0; i < len(a) && j < len(b); {
		if isListingDigit(a[i]) && isListingDigit(b[j]) {
			aStart, bStart := i, j
			for i < len(a) && a[i] == '0' {
				i++
			}
			for j < len(b) && b[j] == '0' {
				j++
			}
			aDigits, bDigits := i, j
			for i < len(a) && isListingDigit(a[i]) {
				i++
			}
			for j < len(b) && isListingDigit(b[j]) {
				j++
			}
			if i-aDigits != j-bDigits {
				if i-aDigits < j-bDigits {
					return -1
				}
				return 1
			}
			if a[aDigits:i] != b[bDigits:j] {
				if a[aDigits:i] < b[bDigits:j] {
					return -1
				}
				return 1
			}
			if i-aStart != j-bStart {
				if i-aStart < j-bStart {
					return -1
				}
				return 1
			}
			continue
		}
		if a[i] != b[j] {
			if a[i] < b[j] {
				return -1
			}
			return 1
		}
		i++
		j++
	}
	if len(a) < len(b) {
		return -1
	}
	if len(a) > len(b) {
		return 1
	}
	return 0
}

func isListingDigit(value byte) bool {
	return value >= '0' && value <= '9'
}

func listingFileType(name string) (string, string) {
	ext := strings.TrimPrefix(strings.ToLower(path.Ext(name)), ".")
	types := map[string]string{
		"pdf": "Dokument PDF", "doc": "Dokument Word", "docx": "Dokument Word", "odt": "Dokument tekstowy", "txt": "Plik tekstowy", "rtf": "Dokument tekstowy",
		"xls": "Arkusz kalkulacyjny", "xlsx": "Arkusz kalkulacyjny", "ods": "Arkusz kalkulacyjny", "csv": "Dane tabelaryczne",
		"ppt": "Prezentacja", "pptx": "Prezentacja", "odp": "Prezentacja",
		"dwg": "Rysunek CAD", "dxf": "Rysunek CAD", "ifc": "Model BIM",
		"jpg": "Obraz JPEG", "jpeg": "Obraz JPEG", "png": "Obraz PNG", "gif": "Obraz GIF", "webp": "Obraz WebP", "tif": "Obraz TIFF", "tiff": "Obraz TIFF", "svg": "Grafika SVG",
		"zip": "Archiwum ZIP", "7z": "Archiwum 7-Zip", "rar": "Archiwum RAR", "tar": "Archiwum TAR", "gz": "Archiwum GZip",
		"mp4": "Wideo", "mkv": "Wideo", "mov": "Wideo", "avi": "Wideo", "webm": "Wideo",
		"mp3": "Dźwięk", "wav": "Dźwięk", "flac": "Dźwięk", "m4a": "Dźwięk", "ogg": "Dźwięk",
	}
	typeName := types[ext]
	if typeName == "" {
		if ext == "" {
			return "Plik", "document"
		}
		typeName = "Plik " + strings.ToUpper(ext)
	}
	icons := map[string]string{
		"pdf": "pdf", "doc": "document", "docx": "document", "odt": "document", "txt": "document", "rtf": "document",
		"xls": "table", "xlsx": "table", "ods": "table", "csv": "table",
		"ppt": "slide", "pptx": "slide", "odp": "slide",
		"dwg": "cad", "dxf": "cad", "ifc": "cad", "stl": "cad", "obj": "cad", "step": "cad", "stp": "cad",
		"jpg": "image", "jpeg": "image", "png": "image", "gif": "image", "webp": "image", "tif": "image", "tiff": "image", "svg": "image",
		"zip": "archive", "7z": "archive", "rar": "archive", "tar": "archive", "gz": "archive",
		"mp4": "video", "mkv": "video", "mov": "video", "avi": "video", "webm": "video",
		"mp3": "audio", "wav": "audio", "flac": "audio", "m4a": "audio", "ogg": "audio",
		"html": "code", "htm": "code", "css": "code", "js": "code", "json": "code", "xml": "code", "go": "code", "py": "code", "sh": "code",
	}
	iconName := icons[ext]
	if iconName == "" {
		iconName = "document"
	}
	return typeName, iconName
}

func formatListingSize(size int64) string {
	if size < 1024 {
		return strconv.FormatInt(size, 10) + " B"
	}
	units := []string{"KB", "MB", "GB", "TB"}
	value := float64(size)
	unit := "B"
	for _, candidate := range units {
		value /= 1024
		unit = candidate
		if value < 1024 {
			break
		}
	}
	precision := 1
	if value >= 10 {
		precision = 0
	}
	return strconv.FormatFloat(value, 'f', precision, 64) + " " + unit
}

func formatListingCount(count int) string {
	word := "plików"
	if count == 1 {
		word = "plik"
	} else if last, lastTwo := count%10, count%100; last >= 2 && last <= 4 && (lastTwo < 12 || lastTwo > 14) {
		word = "pliki"
	}
	return strconv.Itoa(count) + " " + word
}

func (h Handler) renderPassword(w http.ResponseWriter, projection channel.Projection) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	branding, err := realmbranding.Normalize(projection.Branding)
	if err != nil {
		branding = realmbranding.Default()
	}
	ownerInk := "#0B1D3A"
	if branding.LeadingColor != realmbranding.DefaultLeadingColor {
		ownerInk = branding.LeadingColor
	}
	css := passwordCSS + ":root{--owner-accent:" + branding.LeadingColor + ";--owner-ink:" + ownerInk + "}"
	digest := sha256.Sum256([]byte(css))
	cssHash := base64.StdEncoding.EncodeToString(digest[:])
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'sha256-"+cssHash+"'; img-src data:; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
	data := passwordPage{BrandSymbol: brandSymbol, CSS: template.CSS(css)}
	if branding.LogoBase64 != "" {
		data.HasOwnerLogo = true
		data.OwnerLogo = template.URL("data:" + branding.LogoMediaType + ";base64," + branding.LogoBase64)
	}
	_ = passwordTemplate.Execute(w, data)
}

func (h Handler) renderRecipient(w http.ResponseWriter, projection channel.Projection, _ string, codeSent, invalid bool) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	branding, err := realmbranding.Normalize(projection.Branding)
	if err != nil {
		branding = realmbranding.Default()
	}
	ownerInk := "#0B1D3A"
	if branding.LeadingColor != realmbranding.DefaultLeadingColor {
		ownerInk = branding.LeadingColor
	}
	css := passwordCSS + recipientCSS + ":root{--owner-accent:" + branding.LeadingColor + ";--owner-ink:" + ownerInk + "}"
	digest := sha256.Sum256([]byte(css))
	cssHash := base64.StdEncoding.EncodeToString(digest[:])
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'sha256-"+cssHash+"'; img-src data:; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
	data := recipientPage{BrandSymbol: brandSymbol, CSS: template.CSS(css), CodeSent: codeSent, Invalid: invalid}
	if branding.LogoBase64 != "" {
		data.HasOwnerLogo = true
		data.OwnerLogo = template.URL("data:" + branding.LogoMediaType + ";base64," + branding.LogoBase64)
	}
	_ = recipientTemplate.Execute(w, data)
}

type recipientPage struct {
	BrandSymbol  template.HTML
	CSS          template.CSS
	OwnerLogo    template.URL
	HasOwnerLogo bool
	CodeSent     bool
	Invalid      bool
}

type passwordPage struct {
	BrandSymbol  template.HTML
	CSS          template.CSS
	OwnerLogo    template.URL
	HasOwnerLogo bool
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

func visitURL(base, encoded, invitation string) string {
	query := url.Values{"v": []string{encoded}}
	if invitation != "" {
		query.Set("invite", invitation)
	}
	return base + "?" + query.Encode()
}

func latestURL(alias, slug, invitation string) string {
	base := "/" + url.PathEscape(alias) + "/" + url.PathEscape(slug)
	if invitation == "" {
		return base
	}
	return base + "?invite=" + url.QueryEscape(invitation)
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

func (h Handler) notFound(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	digest := sha256.Sum256([]byte(notFoundCSS))
	cssHash := base64.StdEncoding.EncodeToString(digest[:])
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'sha256-"+cssHash+"'; img-src data:; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
	w.WriteHeader(http.StatusNotFound)
	_ = notFoundTemplate.Execute(w, struct {
		BrandSymbol template.HTML
		CSS         template.CSS
	}{BrandSymbol: brandSymbol, CSS: template.CSS(notFoundCSS)})
}
func (h Handler) now() time.Time {
	if h.Now != nil {
		return h.Now().UTC()
	}
	return time.Now().UTC()
}

// The SVGs are a pinned, vendored subset of Microsoft Fluent UI System Icons.
// LICENSE.txt beside them records their MIT license and exact package version.
//
//go:embed assets/fluent/*.svg assets/brand/*.svg
var embeddedWebAssets embed.FS

var brandSymbol = func() template.HTML {
	raw, err := embeddedWebAssets.ReadFile("assets/brand/filees-space-symbol.svg")
	if err != nil || !strings.HasPrefix(strings.TrimSpace(string(raw)), "<svg") {
		panic("invalid embedded filees:space symbol")
	}
	return template.HTML(raw) // Owner-supplied, pinned SVG asset.
}()

var listingIcons = func() map[string]template.HTML {
	files := map[string]string{
		"archive": "archive_24_regular.svg", "audio": "music_note_2_24_regular.svg",
		"cad": "cube_24_regular.svg", "code": "code_24_regular.svg",
		"document": "document_24_regular.svg", "download": "arrow_download_24_regular.svg",
		"folder": "folder_24_filled.svg", "image": "image_24_regular.svg",
		"pdf": "document_pdf_24_regular.svg", "slide": "slide_text_24_regular.svg",
		"table": "table_24_regular.svg", "video": "video_24_regular.svg",
	}
	icons := make(map[string]template.HTML, len(files))
	for name, file := range files {
		raw, err := embeddedWebAssets.ReadFile("assets/fluent/" + file)
		if err != nil || !strings.HasPrefix(strings.TrimSpace(string(raw)), "<svg") {
			panic("invalid embedded Fluent icon: " + file)
		}
		icons[name] = template.HTML(raw) // Pinned, reviewed SVG assets only.
	}
	return icons
}()

func listingIcon(name string) template.HTML {
	if icon := listingIcons[name]; icon != "" {
		return icon
	}
	return listingIcons["document"]
}

const listingCSS = `:root{color-scheme:light;--ink:#171717;--muted:#696969;--line:#dedede;--soft:#f5f5f3;--paper:#fff;--accent:#ffd400;--accent-strong:#e5bd00;--focus:#1666c5;font-family:"Segoe UI",Inter,system-ui,-apple-system,BlinkMacSystemFont,sans-serif}*{box-sizing:border-box}html{background:#ececea}body{margin:0;color:var(--ink);background:linear-gradient(180deg,#f7f7f5 0,#ececea 100%);min-height:100vh}.shell{width:min(1120px,calc(100% - 32px));margin:0 auto;padding:32px 0 56px}.topbar{display:flex;align-items:center;justify-content:space-between;gap:24px;margin-bottom:28px}.brand{display:flex;align-items:center;gap:11px;font-size:15px;font-weight:700;letter-spacing:.02em}.brand-mark{display:grid;place-items:center;width:38px;height:38px;background:var(--ink);color:var(--accent);border-radius:9px;font-size:16px;font-weight:900;letter-spacing:-.08em}.realm{color:var(--muted);font-size:13px}.card{overflow:hidden;background:var(--paper);border:1px solid #d8d8d5;border-radius:14px;box-shadow:0 15px 38px rgba(0,0,0,.08)}.heading{padding:28px 30px 24px;border-bottom:1px solid var(--line)}h1{font-size:clamp(25px,4vw,36px);line-height:1.12;margin:0 0 9px;letter-spacing:-.035em}.meta{display:flex;flex-wrap:wrap;gap:7px 18px;color:var(--muted);font-size:14px}.meta span+span:before{content:"·";margin-right:18px;color:#aaa}.latest-link{color:inherit;text-decoration-thickness:1px;text-underline-offset:3px}.latest-link:hover{color:var(--ink)}.browser{min-width:0}.columns,.file-row{display:grid;grid-template-columns:minmax(260px,1fr) minmax(150px,220px) 100px 96px;align-items:center}.columns{min-height:42px;padding:0 18px;color:#727272;background:var(--soft);border-bottom:1px solid var(--line);font-size:12px;font-weight:650;text-transform:uppercase;letter-spacing:.045em}.columns span:nth-child(3){text-align:right}.columns span:last-child{text-align:right}.tree{padding:7px 0}.directory{border-bottom:1px solid #eee}.directory:last-child{border-bottom:0}.directory summary{position:relative;display:flex;align-items:center;gap:11px;min-height:45px;padding:0 20px;cursor:pointer;font-weight:650;list-style:none;user-select:none}.directory summary::-webkit-details-marker{display:none}.directory summary:before{content:"";width:8px;height:8px;border-right:2px solid #777;border-bottom:2px solid #777;transform:rotate(-45deg);transition:transform .12s ease}.directory[open]>summary:before{transform:rotate(45deg) translate(-2px,-2px)}.folder-icon{position:relative;width:23px;height:16px;border:1px solid #d6ac00;border-radius:3px;background:var(--accent);box-shadow:inset 0 -2px 0 rgba(0,0,0,.06)}.folder-icon:before{content:"";position:absolute;left:1px;top:-5px;width:10px;height:5px;border:1px solid #d6ac00;border-bottom:0;border-radius:3px 3px 0 0;background:var(--accent)}.branch{margin-left:27px;border-left:1px solid #ddd}.branch>.directory>summary{padding-left:18px}.file-row{min-height:48px;padding:5px 18px;border-bottom:1px solid #eee;font-size:14px}.file-row:last-child{border-bottom:0}.file-row:hover{background:#fffbea}.file-name{display:flex;align-items:center;min-width:0;gap:12px}.file-badge{display:grid;place-items:center;flex:0 0 34px;height:38px;border:1px solid #cfcfcb;border-radius:4px;background:linear-gradient(145deg,#fff,#efefec);color:#555;font-size:9px;font-weight:800;letter-spacing:.015em}.name-link{min-width:0;color:var(--ink);font-weight:560;text-decoration:none;overflow-wrap:anywhere}.name-link:hover{text-decoration:underline;text-decoration-thickness:1.5px;text-underline-offset:3px}.file-type{color:var(--muted);overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.file-size{text-align:right;color:#4f4f4f;font-variant-numeric:tabular-nums}.unknown{color:#aaa}.download{text-align:center;justify-self:end;min-width:78px;padding:7px 10px;border:1px solid #c9c9c5;border-radius:7px;color:#222;background:#fff;text-decoration:none;font-size:13px;font-weight:650}.download:hover{border-color:var(--accent-strong);background:var(--accent)}a:focus-visible,summary:focus-visible{outline:3px solid var(--focus);outline-offset:2px}.empty{padding:70px 24px;text-align:center}.empty-icon{display:block;margin:0 auto 17px;width:48px;height:35px;border:2px solid #c4a000;border-radius:6px;background:var(--accent)}.empty strong{display:block;font-size:18px;margin-bottom:6px}.empty p{margin:0;color:var(--muted)}.footer{padding:17px 4px;color:#777;text-align:center;font-size:12px}@media(max-width:720px){.shell{width:min(calc(100% - 18px),1120px);padding-top:16px}.topbar{margin:0 7px 17px}.realm{display:none}.card{border-radius:10px}.heading{padding:22px 18px 19px}.columns,.file-row{grid-template-columns:minmax(150px,1fr) 72px 84px}.columns span:nth-child(2),.file-type{display:none}.branch{margin-left:12px}.directory summary{padding:0 12px}.file-row{padding-left:12px;padding-right:10px}.file-badge{flex-basis:30px;height:34px}.download{min-width:70px}}@media(max-width:450px){.columns,.file-row{grid-template-columns:minmax(130px,1fr) 72px}.columns span:nth-child(3),.file-size{display:none}.meta span+span:before{margin-right:9px}.meta{gap:7px 9px}.branch{margin-left:7px}.file-row{font-size:13px}}@media(prefers-reduced-motion:reduce){.directory summary:before{transition:none}}`

const listingCSSOverrides = `.toolbar{display:flex;align-items:center;justify-content:flex-end;gap:9px;padding:12px 18px;border-bottom:1px solid var(--line);background:#fff}.bundle-button,.folder-download{display:inline-flex;align-items:center;justify-content:center;gap:7px;padding:8px 12px;border:1px solid #c9c9c5;border-radius:7px;color:#222;background:#fff;font:inherit;font-size:13px;font-weight:650;cursor:pointer}.bundle-button.primary{border-color:var(--accent-strong);background:var(--accent)}.bundle-button:hover,.folder-download:hover{border-color:var(--accent-strong);background:#fff6bd}.button-icon,.mime-icon,.folder-icon{display:inline-grid;place-items:center;flex:none}.button-icon svg{width:17px;height:17px}.mime-icon{width:34px;height:38px;color:#4c4c4c}.mime-icon svg{width:26px;height:26px}.folder-icon{width:25px;height:25px;border:0;border-radius:0;background:none;box-shadow:none;color:#e0b900}.folder-icon:before{content:none}.folder-icon svg{width:25px;height:25px}.columns,.file-row{grid-template-columns:34px minmax(260px,1fr) minmax(150px,220px) 100px 96px}.columns span:nth-child(4){text-align:right}.select-cell{display:grid;place-items:center}.select-cell input{width:17px;height:17px;accent-color:var(--accent-strong)}.folder-actions{display:flex;justify-content:flex-end;padding:5px 18px 8px}.notice{margin:12px 18px 0;padding:10px 12px;border-left:4px solid var(--accent-strong);background:#fff8cf;font-size:13px}.file-icon-pdf{color:#b42318}.file-icon-image{color:#16794d}.file-icon-video,.file-icon-audio{color:#6b4bb6}.file-icon-table{color:#107c41}.file-icon-slide{color:#c43e1c}.file-icon-cad{color:#1264a3}button:focus-visible,input:focus-visible{outline:3px solid var(--focus);outline-offset:2px}@media(max-width:720px){.columns,.file-row{grid-template-columns:32px minmax(150px,1fr) 72px 84px}.columns span:nth-child(3),.file-type{display:none}.toolbar{padding:10px;flex-wrap:wrap}.mime-icon{width:30px;height:34px}.mime-icon svg{width:24px;height:24px}}@media(max-width:450px){.columns,.file-row{grid-template-columns:30px minmax(130px,1fr) 72px}.columns span:nth-child(4),.file-size{display:none}.bundle-button{flex:1}.folder-actions{padding-right:10px}}`

const brandCSSOverrides = `:root{--ink:#0B1D3A;--muted:#667085;--line:#D9DEE7;--soft:#F7F8FA;--paper:#FFFFFF;--accent:#FF6A00;--accent-strong:#D95800;--owner-accent:#FF6A00;--owner-ink:#0B1D3A;--focus:#1264A3;--mono:"Roboto Mono","IBM Plex Mono","DejaVu Sans Mono",ui-monospace,SFMono-Regular,Consolas,monospace}html{background:var(--soft)}body{background:var(--soft);color:var(--owner-ink)}.shell{width:min(1200px,calc(100% - 48px));padding:24px 0 48px}.topbar{padding:16px 22px;margin:0;background:var(--paper);border:1px solid var(--line);border-bottom:0;color:var(--ink);font-family:var(--mono);font-size:13px;letter-spacing:.035em}.brand{gap:10px;color:var(--owner-accent);font-size:14px}.brand-mark{display:block;width:31px;height:25px;background:none;border-radius:0;color:inherit}.brand-mark svg{display:block;width:100%;height:100%}.realm{font-family:var(--mono);font-size:12px}.card{border-radius:0;border-color:var(--line);box-shadow:0 18px 50px rgba(11,29,58,.08)}.heading{display:grid;grid-template-columns:minmax(0,1fr) minmax(0,280px);align-items:center;gap:28px;padding:32px 30px 27px;border-bottom-color:var(--line);border-left:5px solid var(--owner-accent)}.heading:not(:has(.owner-logo)){grid-template-columns:1fr}.owner-logo{display:block;justify-self:end;max-width:100%;max-height:88px;width:auto;height:auto;object-fit:contain;object-position:right center}h1{color:var(--owner-ink);font-size:clamp(26px,4vw,38px);letter-spacing:-.03em}.meta{font-family:var(--mono);font-size:12px}.columns{background:var(--soft);color:var(--muted);font-family:var(--mono);font-weight:600}.toolbar{background:var(--paper);padding:13px 18px}.bundle-button,.folder-download{border-color:var(--owner-ink);border-radius:2px;color:var(--owner-ink);background:var(--paper);font-family:var(--mono);font-size:12px;font-weight:600}.bundle-button.primary{border-color:var(--accent);background:var(--accent);color:var(--owner-ink)}.bundle-button:hover,.folder-download:hover,.download:hover{border-color:var(--owner-ink);background:var(--owner-ink);color:#fff}.folder-icon{color:var(--accent)}.mime-icon,.file-icon-pdf,.file-icon-image,.file-icon-video,.file-icon-audio,.file-icon-table,.file-icon-slide,.file-icon-cad{color:var(--owner-ink)}.select-cell input{accent-color:var(--accent)}.download{border-color:var(--line);border-radius:2px;color:var(--owner-ink);font-family:var(--mono);font-size:12px}.notice{border-left-color:var(--accent);background:#FFF3EB;color:var(--owner-ink);font-family:var(--mono)}.directory summary,.name-link{color:var(--owner-ink)}.file-row:hover{background:#FFF8F3}.footer{font-family:var(--mono);color:var(--muted)}@media(max-width:640px){.shell{width:100%;padding:0}.topbar{border-left:0;border-right:0}.card{border-left:0;border-right:0}.heading{grid-template-columns:1fr auto;gap:14px;padding:27px 20px 23px}.owner-logo{max-width:130px;max-height:60px}}@media(max-width:430px){.heading{grid-template-columns:1fr}.owner-logo{justify-self:start;max-width:180px}}`

const iconColorCSS = `.button-icon svg,.mime-icon svg,.folder-icon svg{fill:currentColor}.bundle-button,.folder-download{border-color:var(--ink)}.empty-icon{background:var(--accent);border-color:var(--ink);border-radius:2px}`

var listingCSSHash = func() string {
	digest := sha256.Sum256([]byte(listingCSS + listingCSSOverrides + brandCSSOverrides + iconColorCSS))
	return base64.StdEncoding.EncodeToString(digest[:])
}()

var listingTemplate = template.Must(template.New("listing").Parse(`<!doctype html>
<html lang="pl">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{.Slug}} — pliki do pobrania</title>
<style>{{.CSS}}</style>
</head>
<body>
<main class="shell">
<header class="topbar"><div class="brand"><span class="brand-mark" aria-hidden="true">{{.BrandSymbol}}</span><span>filees:space</span></div><div class="realm">udostępnione przez {{.Alias}}</div></header>
<section class="card" aria-labelledby="share-title">
<div class="heading"><div><h1 id="share-title">{{.Slug}}</h1><div class="meta"><span>{{.CountText}}</span><span>wizyta zamrożona na r{{.Revision}}</span><span><a class="latest-link" href="{{.LatestURL}}">Sprawdź najnowsze wydanie</a></span><span>{{.Alias}}</span></div></div>{{if .HasOwnerLogo}}<img class="owner-logo" src="{{.OwnerLogo}}" alt="Logo udostępniającego">{{end}}</div>
<div class="browser">
{{if eq .Count 0}}
<div class="empty"><span class="empty-icon" aria-hidden="true"></span><strong>Ten folder jest jeszcze pusty</strong><p>Właściciel może dodać tu pliki później.</p></div>
{{else}}
{{if .Bundles}}<form method="post" action="{{.BundleURL}}"><div class="toolbar"><button class="bundle-button" type="submit"><span class="button-icon" aria-hidden="true">{{.DownloadIcon}}</span>Pobierz zaznaczone</button><button class="bundle-button primary" type="submit" name="all" value="1"><span class="button-icon" aria-hidden="true">{{.ArchiveIcon}}</span>Pobierz całość</button></div>{{if .SelectionNotice}}<div class="notice" role="status">Najpierw zaznacz przynajmniej jeden plik.</div>{{end}}{{end}}
<div class="columns" role="row"><span aria-hidden="true"></span><span>Nazwa</span><span>Typ</span><span>Rozmiar</span><span>Pobierz</span></div>
<div class="tree">{{range .Tree.Directories}}{{template "directory" .}}{{end}}{{range .Tree.Files}}{{template "file" .}}{{end}}</div>
{{if .Bundles}}</form>{{end}}
{{end}}
</div>
</section>
<footer class="footer">Bezpieczne udostępnienie FileES</footer>
</main>
</body>
</html>
{{define "directory"}}<details class="directory"><summary><span class="folder-icon" aria-hidden="true">{{.FolderIcon}}</span><span>{{.Name}}</span></summary><div class="branch">{{if .BundleEnabled}}<div class="folder-actions"><button class="folder-download" type="submit" name="folder" value="{{.Path}}"><span class="button-icon" aria-hidden="true">{{.DownloadIcon}}</span>Pobierz folder</button></div>{{end}}{{range .Directories}}{{template "directory" .}}{{end}}{{range .Files}}{{template "file" .}}{{end}}</div></details>{{end}}
{{define "file"}}<div class="file-row" role="row"><div class="select-cell">{{if .BundleEnabled}}<input type="checkbox" name="object" value="{{.PublicID}}" aria-label="Zaznacz {{.Name}}">{{end}}</div><div class="file-name"><span class="mime-icon file-icon-{{.IconClass}}" aria-hidden="true">{{.Icon}}</span><a class="name-link" rel="nofollow" href="{{.URL}}">{{.Name}}</a></div><div class="file-type">{{.Type}}</div><div class="file-size{{if not .SizeKnown}} unknown{{end}}">{{if .SizeKnown}}{{.Size}}{{else}}—{{end}}</div><a class="download" rel="nofollow" href="{{.URL}}">Pobierz</a></div>{{end}}`))

const passwordCSS = `:root{color-scheme:light;--paper:#fff;--soft:#f7f8fa;--line:#d9dee7;--ink:#0b1d3a;--muted:#667085;--owner-accent:#ff6a00;--owner-ink:#0b1d3a;--focus:#1264a3;--mono:"Roboto Mono","IBM Plex Mono","DejaVu Sans Mono",ui-monospace,SFMono-Regular,Consolas,monospace;font-family:"Segoe UI",Inter,system-ui,-apple-system,BlinkMacSystemFont,sans-serif}*{box-sizing:border-box}html{background:var(--soft)}body{margin:0;min-height:100vh;color:var(--owner-ink);background:var(--soft)}.shell{width:min(900px,calc(100% - 48px));margin:0 auto;padding:40px 0 56px}.topbar{padding:16px 22px;background:var(--paper);border:1px solid var(--line);border-bottom:0;font-family:var(--mono);letter-spacing:.035em}.brand{display:flex;align-items:center;gap:10px;color:var(--owner-accent);font-size:14px;font-weight:700}.brand-mark{display:block;width:31px;height:25px;color:inherit}.brand-mark svg{display:block;width:100%;height:100%}.card{display:grid;grid-template-columns:minmax(0,1fr) minmax(190px,280px);gap:44px;align-items:center;padding:46px 48px 43px;background:var(--paper);border:1px solid var(--line);border-left:5px solid var(--owner-accent);box-shadow:0 18px 50px rgba(11,29,58,.08)}.card:not(:has(.owner-logo)){grid-template-columns:minmax(0,560px);justify-content:center}.field-label{display:block;margin-bottom:8px;color:var(--owner-ink);font-family:var(--mono);font-size:12px;font-weight:650}.password-row{display:flex;gap:10px}.password-input{min-width:0;flex:1;height:46px;padding:0 13px;border:1px solid #aeb7c5;border-radius:2px;color:var(--ink);background:#fff;font:inherit;font-size:16px}.submit{min-height:46px;padding:0 20px;border:1px solid var(--owner-ink);border-radius:2px;color:#fff;background:var(--owner-ink);font-family:var(--mono);font-size:12px;font-weight:700;cursor:pointer;white-space:nowrap}.submit:hover{border-color:var(--owner-accent);background:var(--owner-accent)}.password-input:focus-visible,.submit:focus-visible{outline:3px solid var(--focus);outline-offset:2px}.owner-logo{display:block;justify-self:end;max-width:100%;max-height:112px;width:auto;height:auto;object-fit:contain;object-position:right center}.footer{padding:18px 4px;color:var(--muted);text-align:center;font-family:var(--mono);font-size:11px}@media(max-width:700px){.shell{width:min(calc(100% - 24px),900px);padding-top:20px}.card{grid-template-columns:1fr;gap:28px;padding:34px 30px}.owner-logo{justify-self:start;max-width:190px;max-height:72px;grid-row:1}}@media(max-width:460px){.shell{width:100%;padding:0}.topbar,.card{border-left:0;border-right:0}.card{padding:30px 20px;border-left:5px solid var(--owner-accent)}.password-row{display:grid}.submit{width:100%}}`

var passwordTemplate = template.Must(template.New("password").Parse(`<!doctype html>
<html lang="pl">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Dostęp — filees:space</title>
<style>{{.CSS}}</style>
</head>
<body>
<main class="shell">
<header class="topbar"><div class="brand"><span class="brand-mark" aria-hidden="true">{{.BrandSymbol}}</span><span>filees:space</span></div></header>
<section class="card" aria-label="Dostęp">
<div class="content"><form method="post"><label class="field-label" for="share-password">Hasło</label><div class="password-row"><input class="password-input" id="share-password" type="password" name="password" autocomplete="current-password" required autofocus><button class="submit" type="submit">Otwórz</button></div></form></div>
{{if .HasOwnerLogo}}<img class="owner-logo" src="{{.OwnerLogo}}" alt="Logo udostępniającego">{{end}}
</section>
<footer class="footer">Bezpieczne udostępnienie FileES</footer>
</main>
</body>
</html>`))

const recipientCSS = `.hint{margin:0 0 18px;color:var(--muted);font-size:14px;line-height:1.55}.status{margin:0 0 14px;padding:10px 12px;border-left:4px solid var(--owner-accent);background:var(--soft);color:var(--owner-ink);font-family:var(--mono);font-size:12px}.status.error{border-left-color:#b42318;color:#8a1c13}.resend{margin-top:14px}.resend .submit{min-height:38px;padding:0 14px;color:var(--owner-ink);background:#fff}.code-input{font-family:var(--mono);font-size:19px;letter-spacing:.16em}`

var recipientTemplate = template.Must(template.New("recipient").Parse(`<!doctype html>
<html lang="pl">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Dostęp — filees:space</title>
<style>{{.CSS}}</style>
</head>
<body>
<main class="shell">
<header class="topbar"><div class="brand"><span class="brand-mark" aria-hidden="true">{{.BrandSymbol}}</span><span>filees:space</span></div></header>
<section class="card" aria-label="Dostęp">
<div class="content">
{{if .CodeSent}}
{{if .Invalid}}<div class="status error" role="alert">Kod jest nieprawidłowy lub wygasł.</div>{{else}}<div class="status" role="status">Jeżeli zaproszenie jest aktywne, kod został wysłany.</div>{{end}}
<form method="post"><input type="hidden" name="action" value="verify"><label class="field-label" for="share-code">Kod dostępu</label><div class="password-row"><input class="password-input code-input" id="share-code" type="text" name="code" inputmode="numeric" autocomplete="one-time-code" pattern="[0-9]{8}" maxlength="8" required autofocus><button class="submit" type="submit">Otwórz</button></div></form>
<form class="resend" method="post"><button class="submit" type="submit" name="action" value="send">Wyślij kod ponownie</button></form>
{{else}}
<p class="hint">Dostęp wymaga jednorazowego kodu wysłanego na przypisaną skrzynkę.</p>
<form method="post"><button class="submit" type="submit" name="action" value="send">Wyślij kod</button></form>
{{end}}
</div>
{{if .HasOwnerLogo}}<img class="owner-logo" src="{{.OwnerLogo}}" alt="Logo udostępniającego">{{end}}
</section>
<footer class="footer">Bezpieczne udostępnienie FileES</footer>
</main>
</body>
</html>`))

const notFoundCSS = `:root{color-scheme:light;--paper:#fff;--soft:#f7f8fa;--line:#d9dee7;--ink:#0b1d3a;--muted:#667085;--accent:#ff6a00;--focus:#1264a3;--mono:"Roboto Mono","IBM Plex Mono","DejaVu Sans Mono",ui-monospace,SFMono-Regular,Consolas,monospace;font-family:"Segoe UI",Inter,system-ui,-apple-system,BlinkMacSystemFont,sans-serif}*{box-sizing:border-box}html{background:var(--soft)}body{margin:0;min-height:100vh;color:var(--ink);background:var(--soft)}.shell{width:min(900px,calc(100% - 48px));margin:0 auto;padding:40px 0 56px}.topbar{display:flex;align-items:center;justify-content:space-between;padding:16px 22px;background:var(--paper);border:1px solid var(--line);border-bottom:0;font-family:var(--mono);letter-spacing:.035em}.brand{display:flex;align-items:center;gap:10px;color:var(--accent);font-size:14px;font-weight:700}.brand-mark{display:block;width:31px;height:25px;color:inherit}.brand-mark svg{display:block;width:100%;height:100%}.error-code{padding:5px 8px;border:1px solid var(--accent);color:var(--ink);font-size:11px;font-weight:700;letter-spacing:.08em}.card{padding:52px 48px 50px;background:var(--paper);border:1px solid var(--line);border-left:5px solid var(--accent);box-shadow:0 18px 50px rgba(11,29,58,.08)}h1{margin:0 0 13px;font-size:clamp(28px,5vw,42px);line-height:1.12;letter-spacing:-.035em}.subtitle{max-width:620px;margin:0 0 12px;color:var(--muted);font-size:17px;line-height:1.6}.hint{max-width:620px;margin:0;color:var(--ink);font-size:14px;line-height:1.6}.home{display:inline-block;margin-top:27px;padding:11px 15px;border:1px solid var(--ink);border-radius:2px;color:var(--ink);font-family:var(--mono);font-size:12px;font-weight:700;text-decoration:none}.home:hover{border-color:var(--accent);color:#fff;background:var(--accent)}.home:focus-visible{outline:3px solid var(--focus);outline-offset:2px}.footer{padding:18px 4px;color:var(--muted);text-align:center;font-family:var(--mono);font-size:11px}@media(max-width:460px){.shell{width:100%;padding:0}.topbar,.card{border-left:0;border-right:0}.topbar{padding:15px 18px}.card{padding:38px 20px;border-left:5px solid var(--accent)}.error-code{font-size:10px}}`

var notFoundTemplate = template.Must(template.New("not-found").Parse(`<!doctype html>
<html lang="pl">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="robots" content="noindex,nofollow">
<title>Nie znaleziono — filees:space</title>
<style>{{.CSS}}</style>
</head>
<body>
<main class="shell">
<header class="topbar"><div class="brand"><span class="brand-mark" aria-hidden="true">{{.BrandSymbol}}</span><span>filees:space</span></div><span class="error-code" role="status">HTTP ERROR 404</span></header>
<section class="card" aria-labelledby="error-title">
<h1 id="error-title">Przestrzeń niedostępna :(</h1>
<p class="subtitle">Adres jest nieprawidłowy albo udostępnienie wygasło.</p>
<p class="hint">Jeśli to pomyłka, poproś nadawcę o aktualny link.</p>
<a class="home" href="/" rel="nofollow">Strona główna</a>
</section>
<footer class="footer">Bezpieczne udostępnienie FileES</footer>
</main>
</body>
</html>`))
