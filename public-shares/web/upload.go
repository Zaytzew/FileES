package web

import (
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
	"mime/multipart"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"

	"filees/pkg/realmbranding"
	"filees/public-shares/channel"
	"filees/public-shares/gate"
	"filees/public-shares/recipientotp"
	"github.com/google/uuid"
)

const defaultMaxUploadBytes = 64 << 20

func (h Handler) uploadEntry(w http.ResponseWriter, request *http.Request, alias, channelSlug string) {
	if h.Intake == nil || h.Backend == nil {
		h.notFound(w)
		return
	}
	projection, err := h.Backend.InspectUpload(alias, channelSlug)
	if err != nil || projection.Validate() != nil || projection.Alias != alias || projection.Slug != channelSlug {
		h.notFound(w)
		return
	}
	invitation := request.URL.Query().Get("invite")
	if invitation == "" {
		invitation = request.URL.Query().Get("token")
	}
	if request.Method == http.MethodGet {
		h.uploadGet(w, projection, invitation, request.URL.Query().Get("g"))
		return
	}
	if request.Method != http.MethodPost {
		h.notFound(w)
		return
	}
	media, params, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil {
		h.notFound(w)
		return
	}
	if media == "application/x-www-form-urlencoded" {
		h.uploadOTPPost(w, request, projection, invitation)
		return
	}
	if media != "multipart/form-data" || params["boundary"] == "" {
		h.notFound(w)
		return
	}
	h.uploadFilePost(w, request, projection, invitation, params["boundary"])
}

func (h Handler) uploadGet(w http.ResponseWriter, projection channel.UploadProjection, invitation, encodedGrant string) {
	if invitation == "" {
		h.notFound(w)
		return
	}
	if projection.RequireOTP {
		if encodedGrant == "" {
			h.renderUploadOTP(w, projection, false, false, "")
			return
		}
		if err := h.verifyUploadGrant(encodedGrant, projection, invitation); err != nil {
			h.renderUploadOTP(w, projection, false, false, "")
			return
		}
		h.renderUpload(w, projection, false, encodedGrant)
		return
	}
	if !uploadAuthorized(projection, invitation) {
		h.notFound(w)
		return
	}
	h.renderUpload(w, projection, false, "")
}

func (h Handler) uploadOTPPost(w http.ResponseWriter, request *http.Request, projection channel.UploadProjection, invitation string) {
	if !projection.RequireOTP || invitation == "" {
		h.notFound(w)
		return
	}
	request.Body = http.MaxBytesReader(w, request.Body, 8*1024)
	if err := request.ParseForm(); err != nil {
		h.notFound(w)
		return
	}
	email := strings.TrimSpace(request.PostForm.Get("email"))
	action := request.PostForm.Get("action")
	base := recipientotp.Request{Alias: projection.Alias, Slug: projection.Slug, Invitation: invitation, Email: email}
	if action == "send" {
		_ = h.Backend.RequestRecipientOTP(request.Context(), base)
		h.renderUploadOTP(w, projection, true, false, email)
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
		h.renderUploadOTP(w, projection, true, true, email)
		return
	}
	encoded, err := h.signUploadGrant(uploadGrant{
		Version: 1, Kind: uploadGrantKind, ChannelID: projection.ChannelID,
		Subject: "upload:" + digest + ":" + grant.Epoch, ExpiresAt: grant.ExpiresAt.Unix(),
	})
	if err != nil {
		h.notFound(w)
		return
	}
	target := fmt.Sprintf("/%s/%s?invite=%s&g=%s", url.PathEscape(projection.Alias), url.PathEscape(projection.Slug), url.QueryEscape(invitation), url.QueryEscape(encoded))
	http.Redirect(w, request, target, http.StatusSeeOther)
}

func (h Handler) uploadFilePost(w http.ResponseWriter, request *http.Request, projection channel.UploadProjection, invitation, boundary string) {
	max := h.maxUploadBytes()
	request.Body = http.MaxBytesReader(w, request.Body, max+1<<20)
	encodedGrant := request.URL.Query().Get("g")
	if projection.RequireOTP {
		if err := h.verifyUploadGrant(encodedGrant, projection, invitation); err != nil {
			h.notFound(w)
			return
		}
	}
	reader := multipart.NewReader(request.Body, boundary)
	accepted := false
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			h.notFound(w)
			return
		}
		switch part.FormName() {
		case "invite", "token":
			raw, readErr := io.ReadAll(io.LimitReader(part, 8<<10))
			part.Close()
			if readErr != nil {
				h.notFound(w)
				return
			}
			if invitation == "" {
				invitation = strings.TrimSpace(string(raw))
			}
		case "g":
			raw, readErr := io.ReadAll(io.LimitReader(part, 8<<10))
			part.Close()
			if readErr != nil {
				h.notFound(w)
				return
			}
			if encodedGrant == "" {
				encodedGrant = strings.TrimSpace(string(raw))
			}
		case "file":
			authorized := uploadAuthorized(projection, invitation)
			if projection.RequireOTP {
				authorized = h.verifyUploadGrant(encodedGrant, projection, invitation) == nil && authorized
			}
			if accepted || !authorized {
				part.Close()
				h.notFound(w)
				return
			}
			original := filepath.Base(part.FileName())
			store := *h.Intake
			if store.MaxBytes < 1 {
				store.MaxBytes = max
			}
			_, err = store.Accept(projection.ChannelID, projection.Alias, projection.Slug, gate.TokenHash(invitation), original, part)
			part.Close()
			if err != nil {
				h.notFound(w)
				return
			}
			accepted = true
		default:
			part.Close()
		}
	}
	if !accepted {
		h.notFound(w)
		return
	}
	h.renderUpload(w, projection, true, "")
}

const uploadGrantKind = "upload"

type uploadGrant struct {
	Version   int    `json:"v"`
	Kind      string `json:"k"`
	ChannelID string `json:"channel_id"`
	Subject   string `json:"subject"`
	ExpiresAt int64  `json:"expires_at"`
}

func (h Handler) signUploadGrant(claims uploadGrant) (string, error) {
	raw, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(raw)
	mac := hmac.New(sha256.New, h.VisitKey)
	_, _ = io.WriteString(mac, payload)
	return payload + "." + hex.EncodeToString(mac.Sum(nil)), nil
}

func (h Handler) verifyUploadGrant(encoded string, projection channel.UploadProjection, invitation string) error {
	parts := strings.Split(encoded, ".")
	if len(parts) != 2 || len(parts[1]) != sha256.Size*2 {
		return errors.New("upload grant is malformed")
	}
	want, err := hex.DecodeString(parts[1])
	if err != nil {
		return err
	}
	mac := hmac.New(sha256.New, h.VisitKey)
	_, _ = io.WriteString(mac, parts[0])
	if !hmac.Equal(want, mac.Sum(nil)) {
		return errors.New("upload grant signature is invalid")
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(parts[0])
	if err != nil {
		return err
	}
	var claims uploadGrant
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&claims); err != nil {
		return errors.New("upload grant claims are invalid")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) || claims.Version != 1 || claims.Kind != uploadGrantKind || claims.ChannelID != projection.ChannelID || h.now().Unix() >= claims.ExpiresAt {
		return errors.New("upload grant claims are invalid")
	}
	subject := strings.Split(claims.Subject, ":")
	if len(subject) != 3 || subject[0] != "upload" || subject[2] == "" {
		return errors.New("upload grant subject is invalid")
	}
	if _, err := uuid.Parse(subject[2]); err != nil {
		return errors.New("upload grant epoch is invalid")
	}
	digest := gate.TokenHash(invitation)
	if subtle.ConstantTimeCompare([]byte(subject[1]), []byte(digest)) != 1 || !uploadAuthorized(projection, invitation) {
		return errors.New("upload grant is no longer authorized")
	}
	return nil
}

func uploadAuthorized(projection channel.UploadProjection, invitation string) bool {
	if invitation == "" || len(projection.Recipients) == 0 {
		return false
	}
	want := []byte(gate.TokenHash(invitation))
	matched := false
	for _, recipient := range projection.Recipients {
		if subtle.ConstantTimeCompare([]byte(recipient.InvitationHash), want) == 1 {
			matched = true
		}
	}
	return matched
}

func (h Handler) maxUploadBytes() int64 {
	if h.MaxUploadBytes > 0 {
		return h.MaxUploadBytes
	}
	if h.Intake != nil && h.Intake.MaxBytes > 0 {
		return h.Intake.MaxBytes
	}
	return defaultMaxUploadBytes
}

func (h Handler) renderUpload(w http.ResponseWriter, projection channel.UploadProjection, accepted bool, grant string) {
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
	css := passwordCSS + uploadCSS + ":root{--owner-accent:" + branding.LeadingColor + ";--owner-ink:" + ownerInk + "}"
	digest := sha256.Sum256([]byte(css))
	cssHash := base64.StdEncoding.EncodeToString(digest[:])
	csp := "default-src 'none'; style-src 'sha256-" + cssHash + "'; img-src data:; form-action 'self'; base-uri 'none'; frame-ancestors 'none'"
	if !accepted {
		csp += "; script-src 'sha256-" + uploadFormScriptHash() + "'"
	}
	w.Header().Set("Content-Security-Policy", csp)
	data := uploadPage{BrandSymbol: brandSymbol, CSS: template.CSS(css), Accepted: accepted, Grant: grant}
	if !accepted {
		data.FormScript = template.JS(uploadFormJS)
	}
	if branding.LogoBase64 != "" {
		data.HasOwnerLogo = true
		data.OwnerLogo = template.URL("data:" + branding.LogoMediaType + ";base64," + branding.LogoBase64)
	}
	status := http.StatusOK
	if accepted {
		status = http.StatusAccepted
	}
	w.WriteHeader(status)
	_ = uploadTemplate.Execute(w, data)
}

func (h Handler) renderUploadOTP(w http.ResponseWriter, projection channel.UploadProjection, codeSent, invalid bool, email string) {
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
	data := uploadOTPPage{BrandSymbol: brandSymbol, CSS: template.CSS(css), CodeSent: codeSent, Invalid: invalid, Email: email}
	if branding.LogoBase64 != "" {
		data.HasOwnerLogo = true
		data.OwnerLogo = template.URL("data:" + branding.LogoMediaType + ";base64," + branding.LogoBase64)
	}
	_ = uploadOTPTemplate.Execute(w, data)
}

type uploadPage struct {
	BrandSymbol  template.HTML
	CSS          template.CSS
	FormScript   template.JS
	OwnerLogo    template.URL
	HasOwnerLogo bool
	Accepted     bool
	Grant        string
}

type uploadOTPPage struct {
	BrandSymbol  template.HTML
	CSS          template.CSS
	OwnerLogo    template.URL
	HasOwnerLogo bool
	CodeSent     bool
	Invalid      bool
	Email        string
}

const uploadCSS = `.drop{position:relative;display:flex;flex-direction:column;align-items:center;justify-content:center;gap:8px;min-height:148px;margin:0 0 14px;padding:22px 18px;border:2px dashed #aeb7c5;border-radius:2px;background:var(--soft);color:var(--owner-ink);text-align:center;cursor:pointer}.drop:hover,.drop:focus-within,.drop[data-over="1"]{border-color:var(--owner-accent);background:#fff}.drop-title{font-family:var(--mono);font-size:13px;font-weight:650;line-height:1.45}.drop-name{max-width:100%;color:var(--muted);font-size:13px;line-height:1.4;word-break:break-all}.drop-name:empty{display:none}.file-sr{position:absolute;width:1px;height:1px;padding:0;margin:-1px;overflow:hidden;clip:rect(0,0,0,0);white-space:nowrap;border:0}.file-sr:focus-visible+ .drop-title{outline:3px solid var(--focus);outline-offset:4px}.hint{margin:12px 0 0;color:var(--muted);font-size:14px;line-height:1.55}.status{margin:0 0 18px;color:var(--owner-ink);font-family:var(--mono);font-size:14px}.pending{display:none;align-items:center;gap:12px;margin:0;color:var(--owner-ink);font-family:var(--mono);font-size:14px;line-height:1.45}.spinner{flex:0 0 auto;width:22px;height:22px;border:3px solid var(--line);border-top-color:var(--owner-accent);border-radius:50%;animation:filees-spin .8s linear infinite}@keyframes filees-spin{to{transform:rotate(360deg)}}form[data-busy="1"] .field-label,form[data-busy="1"] .drop,form[data-busy="1"] .password-row,form[data-busy="1"] .hint{display:none}form[data-busy="1"] .pending{display:flex}@media(prefers-reduced-motion:reduce){.spinner{animation:none;border-color:var(--owner-accent)}}`

// uploadFormJS is the only script on the public intake form. It is hashed into
// CSP (no unsafe-inline). It must stay ASCII and must not contain "</".
const uploadFormJS = `(function(){var f=document.getElementById("upload-form");var input=document.getElementById("upload-file");var drop=document.getElementById("upload-drop");var name=document.getElementById("upload-name");if(!f||!input||!drop)return;function busy(e){if(f.getAttribute("data-busy")==="1"){if(e)e.preventDefault();return false}f.setAttribute("data-busy","1");f.setAttribute("aria-busy","true");return true}function showName(){if(!name)return;name.textContent=(input.files&&input.files[0]&&input.files[0].name)?input.files[0].name:""}function takeFile(file){if(!file)return false;try{var dt=new DataTransfer();dt.items.add(file);input.files=dt.files}catch(err){return false}showName();return !!(input.files&&input.files[0])}function firstFile(dt){if(!dt)return null;if(dt.items&&dt.items.length){for(var i=0;i<dt.items.length;i++){var it=dt.items[i];if(it.kind!=="file")continue;if(it.webkitGetAsEntry){var ent=it.webkitGetAsEntry();if(ent&&ent.isDirectory)continue}var file=it.getAsFile();if(file)return file}}if(dt.files&&dt.files.length)return dt.files[0];return null}var depth=0;f.addEventListener("submit",function(e){busy(e)});input.addEventListener("change",showName);drop.addEventListener("dragenter",function(e){e.preventDefault();depth++;drop.setAttribute("data-over","1")});drop.addEventListener("dragover",function(e){e.preventDefault();if(e.dataTransfer)e.dataTransfer.dropEffect="copy"});drop.addEventListener("dragleave",function(){depth--;if(depth<=0){depth=0;drop.removeAttribute("data-over")}});drop.addEventListener("drop",function(e){e.preventDefault();depth=0;drop.removeAttribute("data-over");if(f.getAttribute("data-busy")==="1")return;if(!takeFile(firstFile(e.dataTransfer)))return;if(f.requestSubmit)f.requestSubmit();else{busy();f.submit()}})})();`

func uploadFormScriptHash() string {
	digest := sha256.Sum256([]byte(uploadFormJS))
	return base64.StdEncoding.EncodeToString(digest[:])
}

var uploadTemplate = template.Must(template.New("upload").Parse(`<!doctype html>
<html lang="pl">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Przesyłka — filees:space</title>
<style>{{.CSS}}</style>
</head>
<body>
<main class="shell">
<header class="topbar"><div class="brand"><span class="brand-mark" aria-hidden="true">{{.BrandSymbol}}</span><span>filees:space</span></div></header>
<section class="card" aria-label="Przesyłka">
<div class="content">
{{if .Accepted}}
<p class="status">Plik został przyjęty.</p>
{{else}}
<form id="upload-form" method="post" enctype="multipart/form-data">{{if .Grant}}<input type="hidden" name="g" value="{{.Grant}}">{{end}}<span class="field-label" id="upload-label">Plik</span><label class="drop" id="upload-drop" for="upload-file"><input class="file-sr" id="upload-file" type="file" name="file" required autofocus aria-labelledby="upload-label"><span class="drop-title">Upuść plik albo kliknij, żeby wybrać</span><span class="drop-name" id="upload-name"></span></label><div class="password-row"><button class="submit" type="submit">Wyślij</button></div><p class="hint">Przy większym pliku wysyłka może potrwać. Nie zamykaj karty.</p><p class="pending" role="status" aria-live="assertive"><span class="spinner" aria-hidden="true"></span><span>Wysyłanie pliku… Czekaj na potwierdzenie przyjęcia.</span></p></form>
<script>{{.FormScript}}</script>
{{end}}
</div>
{{if .HasOwnerLogo}}<img class="owner-logo" src="{{.OwnerLogo}}" alt="Logo przyjmującego">{{end}}
</section>
<footer class="footer">Bezpieczne przyjęcie FileES</footer>
</main>
</body>
</html>`))

var uploadOTPTemplate = template.Must(template.New("upload-otp").Parse(`<!doctype html>
<html lang="pl">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Przesyłka — filees:space</title>
<style>{{.CSS}}</style>
</head>
<body>
<main class="shell">
<header class="topbar"><div class="brand"><span class="brand-mark" aria-hidden="true">{{.BrandSymbol}}</span><span>filees:space</span></div></header>
<section class="card" aria-label="Przesyłka">
<div class="content">
{{if .CodeSent}}
{{if .Invalid}}<div class="status error" role="alert">Kod jest nieprawidłowy lub wygasł.</div>{{else}}<div class="status" role="status">Jeżeli zaproszenie jest aktywne, kod został wysłany.</div>{{end}}
<form method="post"><input type="hidden" name="action" value="verify"><input type="hidden" name="email" value="{{.Email}}"><label class="field-label" for="upload-code">Kod dostępu</label><div class="password-row"><input class="password-input code-input" id="upload-code" type="text" name="code" inputmode="numeric" autocomplete="one-time-code" pattern="[0-9]{8}" maxlength="8" required autofocus><button class="submit" type="submit">Dalej</button></div></form>
<form class="resend" method="post"><input type="hidden" name="email" value="{{.Email}}"><button class="submit" type="submit" name="action" value="send">Wyślij kod ponownie</button></form>
{{else}}
<p class="hint">Wniesienie wymaga adresu e-mail z zaproszenia i jednorazowego kodu wysłanego na tę skrzynkę.</p>
<form method="post"><input type="hidden" name="action" value="send"><label class="field-label" for="upload-email">Adres e-mail</label><div class="password-row"><input class="password-input" id="upload-email" type="email" name="email" autocomplete="email" maxlength="254" required autofocus><button class="submit" type="submit">Wyślij kod</button></div></form>
{{end}}
</div>
{{if .HasOwnerLogo}}<img class="owner-logo" src="{{.OwnerLogo}}" alt="Logo przyjmującego">{{end}}
</section>
<footer class="footer">Bezpieczne przyjęcie FileES</footer>
</main>
</body>
</html>`))
