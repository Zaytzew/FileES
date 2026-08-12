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
	"path/filepath"
	"sort"
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
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'sha256-"+listingCSSHash+"'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
	data := listingPage{
		Alias:     projection.Alias,
		Slug:      projection.Slug,
		Revision:  claims.Revision,
		Count:     len(projection.Objects),
		CountText: formatListingCount(len(projection.Objects)),
		Tree:      buildListingTree(projection, encoded),
		CSS:       template.CSS(listingCSS),
	}
	_ = listingTemplate.Execute(w, data)
}

type listingPage struct {
	Alias     string
	Slug      string
	Revision  int64
	Count     int
	CountText string
	Tree      listingDirectory
	CSS       template.CSS
}

type listingDirectory struct {
	Name        string
	Directories []listingDirectory
	Files       []listingFile
}

type listingFile struct {
	Name, Type, Badge, URL, Size string
	SizeKnown                    bool
}

type listingTreeBuilder struct {
	name        string
	directories map[string]*listingTreeBuilder
	files       []listingFile
}

func buildListingTree(projection channel.Projection, visit string) listingDirectory {
	root := &listingTreeBuilder{directories: map[string]*listingTreeBuilder{}}
	for _, object := range projection.Objects {
		parts := listingPathParts(object.DisplayName)
		directory := root
		for _, part := range parts[:len(parts)-1] {
			child := directory.directories[part]
			if child == nil {
				child = &listingTreeBuilder{name: part, directories: map[string]*listingTreeBuilder{}}
				directory.directories[part] = child
			}
			directory = child
		}
		typeName, badge := listingFileType(parts[len(parts)-1])
		file := listingFile{
			Name:  parts[len(parts)-1],
			Type:  typeName,
			Badge: badge,
			URL:   fmt.Sprintf("/%s/%s/get/%s?v=%s", url.PathEscape(projection.Alias), url.PathEscape(projection.Slug), url.PathEscape(object.PublicID), url.QueryEscape(visit)),
		}
		if object.Size != nil {
			file.SizeKnown = true
			file.Size = formatListingSize(*object.Size)
		}
		directory.files = append(directory.files, file)
	}
	return freezeListingTree(root)
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

func freezeListingTree(node *listingTreeBuilder) listingDirectory {
	result := listingDirectory{Name: node.name, Files: append([]listingFile(nil), node.files...)}
	for _, child := range node.directories {
		result.Directories = append(result.Directories, freezeListingTree(child))
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
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(name)), ".")
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
			return "Plik", "FILE"
		}
		typeName = "Plik " + strings.ToUpper(ext)
	}
	badge := strings.ToUpper(ext)
	if len(badge) > 4 {
		badge = badge[:4]
	}
	return typeName, badge
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

const listingCSS = `:root{color-scheme:light;--ink:#171717;--muted:#696969;--line:#dedede;--soft:#f5f5f3;--paper:#fff;--accent:#ffd400;--accent-strong:#e5bd00;--focus:#1666c5;font-family:"Segoe UI",Inter,system-ui,-apple-system,BlinkMacSystemFont,sans-serif}*{box-sizing:border-box}html{background:#ececea}body{margin:0;color:var(--ink);background:linear-gradient(180deg,#f7f7f5 0,#ececea 100%);min-height:100vh}.shell{width:min(1120px,calc(100% - 32px));margin:0 auto;padding:32px 0 56px}.topbar{display:flex;align-items:center;justify-content:space-between;gap:24px;margin-bottom:28px}.brand{display:flex;align-items:center;gap:11px;font-size:15px;font-weight:700;letter-spacing:.02em}.brand-mark{display:grid;place-items:center;width:38px;height:38px;background:var(--ink);color:var(--accent);border-radius:9px;font-size:16px;font-weight:900;letter-spacing:-.08em}.realm{color:var(--muted);font-size:13px}.card{overflow:hidden;background:var(--paper);border:1px solid #d8d8d5;border-radius:14px;box-shadow:0 15px 38px rgba(0,0,0,.08)}.heading{padding:28px 30px 24px;border-bottom:1px solid var(--line)}h1{font-size:clamp(25px,4vw,36px);line-height:1.12;margin:0 0 9px;letter-spacing:-.035em}.meta{display:flex;flex-wrap:wrap;gap:7px 18px;color:var(--muted);font-size:14px}.meta span+span:before{content:"·";margin-right:18px;color:#aaa}.browser{min-width:0}.columns,.file-row{display:grid;grid-template-columns:minmax(260px,1fr) minmax(150px,220px) 100px 96px;align-items:center}.columns{min-height:42px;padding:0 18px;color:#727272;background:var(--soft);border-bottom:1px solid var(--line);font-size:12px;font-weight:650;text-transform:uppercase;letter-spacing:.045em}.columns span:nth-child(3){text-align:right}.columns span:last-child{text-align:right}.tree{padding:7px 0}.directory{border-bottom:1px solid #eee}.directory:last-child{border-bottom:0}.directory summary{position:relative;display:flex;align-items:center;gap:11px;min-height:45px;padding:0 20px;cursor:pointer;font-weight:650;list-style:none;user-select:none}.directory summary::-webkit-details-marker{display:none}.directory summary:before{content:"";width:8px;height:8px;border-right:2px solid #777;border-bottom:2px solid #777;transform:rotate(-45deg);transition:transform .12s ease}.directory[open]>summary:before{transform:rotate(45deg) translate(-2px,-2px)}.folder-icon{position:relative;width:23px;height:16px;border:1px solid #d6ac00;border-radius:3px;background:var(--accent);box-shadow:inset 0 -2px 0 rgba(0,0,0,.06)}.folder-icon:before{content:"";position:absolute;left:1px;top:-5px;width:10px;height:5px;border:1px solid #d6ac00;border-bottom:0;border-radius:3px 3px 0 0;background:var(--accent)}.branch{margin-left:27px;border-left:1px solid #ddd}.branch>.directory>summary{padding-left:18px}.file-row{min-height:48px;padding:5px 18px;border-bottom:1px solid #eee;font-size:14px}.file-row:last-child{border-bottom:0}.file-row:hover{background:#fffbea}.file-name{display:flex;align-items:center;min-width:0;gap:12px}.file-badge{display:grid;place-items:center;flex:0 0 34px;height:38px;border:1px solid #cfcfcb;border-radius:4px;background:linear-gradient(145deg,#fff,#efefec);color:#555;font-size:9px;font-weight:800;letter-spacing:.015em}.name-link{min-width:0;color:var(--ink);font-weight:560;text-decoration:none;overflow-wrap:anywhere}.name-link:hover{text-decoration:underline;text-decoration-thickness:1.5px;text-underline-offset:3px}.file-type{color:var(--muted);overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.file-size{text-align:right;color:#4f4f4f;font-variant-numeric:tabular-nums}.unknown{color:#aaa}.download{text-align:center;justify-self:end;min-width:78px;padding:7px 10px;border:1px solid #c9c9c5;border-radius:7px;color:#222;background:#fff;text-decoration:none;font-size:13px;font-weight:650}.download:hover{border-color:var(--accent-strong);background:var(--accent)}a:focus-visible,summary:focus-visible{outline:3px solid var(--focus);outline-offset:2px}.empty{padding:70px 24px;text-align:center}.empty-icon{display:block;margin:0 auto 17px;width:48px;height:35px;border:2px solid #c4a000;border-radius:6px;background:var(--accent)}.empty strong{display:block;font-size:18px;margin-bottom:6px}.empty p{margin:0;color:var(--muted)}.footer{padding:17px 4px;color:#777;text-align:center;font-size:12px}@media(max-width:720px){.shell{width:min(calc(100% - 18px),1120px);padding-top:16px}.topbar{margin:0 7px 17px}.realm{display:none}.card{border-radius:10px}.heading{padding:22px 18px 19px}.columns,.file-row{grid-template-columns:minmax(150px,1fr) 72px 84px}.columns span:nth-child(2),.file-type{display:none}.branch{margin-left:12px}.directory summary{padding:0 12px}.file-row{padding-left:12px;padding-right:10px}.file-badge{flex-basis:30px;height:34px}.download{min-width:70px}}@media(max-width:450px){.columns,.file-row{grid-template-columns:minmax(130px,1fr) 72px}.columns span:nth-child(3),.file-size{display:none}.meta span+span:before{margin-right:9px}.meta{gap:7px 9px}.branch{margin-left:7px}.file-row{font-size:13px}}@media(prefers-reduced-motion:reduce){.directory summary:before{transition:none}}`

var listingCSSHash = func() string {
	digest := sha256.Sum256([]byte(listingCSS))
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
<header class="topbar"><div class="brand"><span class="brand-mark" aria-hidden="true">F/</span><span>FileES</span></div><div class="realm">Udostępnione przez {{.Alias}}</div></header>
<section class="card" aria-labelledby="share-title">
<div class="heading"><h1 id="share-title">{{.Slug}}</h1><div class="meta"><span>{{.CountText}}</span><span>wydanie r{{.Revision}}</span><span>{{.Alias}}</span></div></div>
<div class="browser">
{{if eq .Count 0}}
<div class="empty"><span class="empty-icon" aria-hidden="true"></span><strong>Ten folder jest jeszcze pusty</strong><p>Właściciel może dodać tu pliki później.</p></div>
{{else}}
<div class="columns" role="row"><span>Nazwa</span><span>Typ</span><span>Rozmiar</span><span>Pobierz</span></div>
<div class="tree">{{range .Tree.Directories}}{{template "directory" .}}{{end}}{{range .Tree.Files}}{{template "file" .}}{{end}}</div>
{{end}}
</div>
</section>
<footer class="footer">Bezpieczne udostępnienie FileES</footer>
</main>
</body>
</html>
{{define "directory"}}<details class="directory" open><summary><span class="folder-icon" aria-hidden="true"></span><span>{{.Name}}</span></summary><div class="branch">{{range .Directories}}{{template "directory" .}}{{end}}{{range .Files}}{{template "file" .}}{{end}}</div></details>{{end}}
{{define "file"}}<div class="file-row" role="row"><div class="file-name"><span class="file-badge" aria-hidden="true">{{.Badge}}</span><a class="name-link" rel="nofollow" href="{{.URL}}">{{.Name}}</a></div><div class="file-type">{{.Type}}</div><div class="file-size{{if not .SizeKnown}} unknown{{end}}">{{if .SizeKnown}}{{.Size}}{{else}}—{{end}}</div><a class="download" rel="nofollow" href="{{.URL}}">Pobierz</a></div>{{end}}`))
var passwordTemplate = template.Must(template.New("password").Parse(`<!doctype html><html lang="pl"><meta charset="utf-8"><title>Dostęp do plików</title><form method="post" action="{{.Action}}"><label>Hasło <input type="password" name="password" autocomplete="current-password" required></label><button type="submit">Otwórz</button></form></html>`))
