package web

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"html/template"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strings"

	"filees/pkg/realmbranding"
	"filees/public-shares/channel"
	"filees/public-shares/gate"
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
		if !uploadAuthorized(projection, invitation) || projection.RequireOTP {
			h.notFound(w)
			return
		}
		h.renderUpload(w, projection, false)
		return
	}
	if request.Method != http.MethodPost {
		h.notFound(w)
		return
	}
	max := h.maxUploadBytes()
	request.Body = http.MaxBytesReader(w, request.Body, max+1<<20)
	media, params, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || media != "multipart/form-data" || params["boundary"] == "" {
		h.notFound(w)
		return
	}
	reader := multipart.NewReader(request.Body, params["boundary"])
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
		case "file":
			if accepted || !uploadAuthorized(projection, invitation) || projection.RequireOTP {
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
	h.renderUpload(w, projection, true)
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

func (h Handler) renderUpload(w http.ResponseWriter, projection channel.UploadProjection, accepted bool) {
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
		csp += "; script-src 'sha256-" + uploadBusyScriptHash() + "'"
	}
	w.Header().Set("Content-Security-Policy", csp)
	data := uploadPage{BrandSymbol: brandSymbol, CSS: template.CSS(css), Accepted: accepted}
	if !accepted {
		data.BusyScript = template.JS(uploadBusyJS)
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

type uploadPage struct {
	BrandSymbol  template.HTML
	CSS          template.CSS
	BusyScript   template.JS
	OwnerLogo    template.URL
	HasOwnerLogo bool
	Accepted     bool
}

const uploadCSS = `.file-input{min-width:0;flex:1;height:46px;padding:10px 13px;border:1px solid #aeb7c5;border-radius:2px;background:#fff;font:inherit;font-size:16px}.file-input:focus-visible{outline:3px solid var(--focus);outline-offset:2px}.hint{margin:12px 0 0;color:var(--muted);font-size:14px;line-height:1.55}.status{margin:0 0 18px;color:var(--owner-ink);font-family:var(--mono);font-size:14px}.pending{display:none;align-items:center;gap:12px;margin:0;color:var(--owner-ink);font-family:var(--mono);font-size:14px;line-height:1.45}.spinner{flex:0 0 auto;width:22px;height:22px;border:3px solid var(--line);border-top-color:var(--owner-accent);border-radius:50%;animation:filees-spin .8s linear infinite}@keyframes filees-spin{to{transform:rotate(360deg)}}form[data-busy="1"] .field-label,form[data-busy="1"] .password-row,form[data-busy="1"] .hint{display:none}form[data-busy="1"] .pending{display:flex}@media(prefers-reduced-motion:reduce){.spinner{animation:none;border-color:var(--owner-accent)}}`

// uploadBusyJS is the only script on the public intake form. It is hashed into
// CSP (no unsafe-inline). It must stay ASCII and must not contain "</".
const uploadBusyJS = `(function(){var f=document.getElementById("upload-form");if(!f)return;f.addEventListener("submit",function(e){if(f.getAttribute("data-busy")==="1"){e.preventDefault();return}f.setAttribute("data-busy","1");f.setAttribute("aria-busy","true")});})();`

func uploadBusyScriptHash() string {
	digest := sha256.Sum256([]byte(uploadBusyJS))
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
<form id="upload-form" method="post" enctype="multipart/form-data"><label class="field-label" for="upload-file">Plik</label><div class="password-row"><input class="file-input" id="upload-file" type="file" name="file" required autofocus><button class="submit" type="submit">Wyślij</button></div><p class="hint">Przy większym pliku wysyłka może potrwać. Nie zamykaj karty.</p><p class="pending" role="status" aria-live="assertive"><span class="spinner" aria-hidden="true"></span><span>Wysyłanie pliku… Czekaj na potwierdzenie przyjęcia.</span></p></form>
<script>{{.BusyScript}}</script>
{{end}}
</div>
{{if .HasOwnerLogo}}<img class="owner-logo" src="{{.OwnerLogo}}" alt="Logo przyjmującego">{{end}}
</section>
<footer class="footer">Bezpieczne przyjęcie FileES</footer>
</main>
</body>
</html>`))
