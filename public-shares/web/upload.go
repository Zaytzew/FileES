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
		csp += "; script-src 'sha256-" + uploadFormScriptHash() + "'"
	}
	w.Header().Set("Content-Security-Policy", csp)
	data := uploadPage{BrandSymbol: brandSymbol, CSS: template.CSS(css), Accepted: accepted}
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

type uploadPage struct {
	BrandSymbol  template.HTML
	CSS          template.CSS
	FormScript   template.JS
	OwnerLogo    template.URL
	HasOwnerLogo bool
	Accepted     bool
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
<form id="upload-form" method="post" enctype="multipart/form-data"><span class="field-label" id="upload-label">Plik</span><label class="drop" id="upload-drop" for="upload-file"><input class="file-sr" id="upload-file" type="file" name="file" required autofocus aria-labelledby="upload-label"><span class="drop-title">Upuść plik albo kliknij, żeby wybrać</span><span class="drop-name" id="upload-name"></span></label><div class="password-row"><button class="submit" type="submit">Wyślij</button></div><p class="hint">Przy większym pliku wysyłka może potrwać. Nie zamykaj karty.</p><p class="pending" role="status" aria-live="assertive"><span class="spinner" aria-hidden="true"></span><span>Wysyłanie pliku… Czekaj na potwierdzenie przyjęcia.</span></p></form>
<script>{{.FormScript}}</script>
{{end}}
</div>
{{if .HasOwnerLogo}}<img class="owner-logo" src="{{.OwnerLogo}}" alt="Logo przyjmującego">{{end}}
</section>
<footer class="footer">Bezpieczne przyjęcie FileES</footer>
</main>
</body>
</html>`))
