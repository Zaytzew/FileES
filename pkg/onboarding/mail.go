package onboarding

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"mime/multipart"
	"net/textproto"
	"strings"

	"filees/pkg/realmbranding"
)

const logoContentID = "filees-mail-logo"

func RenderMail(job MailJob, from, messageIDDomain string, branding realmbranding.Branding) ([]byte, error) {
	canonicalFrom, err := canonicalEmail(from)
	if err != nil {
		return nil, fmt.Errorf("SMTP from: %w", err)
	}
	if !validDomain(messageIDDomain) {
		return nil, errors.New("message_id_domain must be a DNS name")
	}
	entry := job.Entry
	if entry.DeliveryState != DeliverySending || entry.DeliveryAddress == "" {
		return nil, errors.New("mail job does not contain a claimed onboarding message")
	}
	to, err := canonicalEmail(entry.DeliveryAddress)
	if err != nil {
		return nil, fmt.Errorf("SMTP recipient: %w", err)
	}
	if strings.ContainsAny(entry.MessageID, "<>@\r\n") {
		return nil, errors.New("invalid mail message_id")
	}
	branding, err = realmbranding.Normalize(branding)
	if err != nil {
		return nil, fmt.Errorf("mail branding: %w", err)
	}
	var subject, intro, code, footer string
	switch entry.Template {
	case OTPMailTemplate, "":
		if entry.OTP == "" {
			return nil, errors.New("OTP mail job has no OTP")
		}
		subject = "FileES onboarding code"
		intro = "Your one-time FileES tunnel code is:"
		code = entry.OTP
		footer = fmt.Sprintf("It expires at %s. It authorizes one reverse tunnel only.", job.ExpiresAt.UTC().Format("2006-01-02 15:04:05Z"))
	case InvitationMailTemplate:
		if entry.Invitation == "" {
			return nil, errors.New("invitation mail job has no invitation")
		}
		subject = "FileES activation invitation"
		intro = "Open FileES and paste this activation invitation:"
		code = entry.Invitation
		footer = fmt.Sprintf("The invitation expires at %s. It does not activate anything until you confirm the separate one-time code sent later.", job.ExpiresAt.UTC().Format("2006-01-02 15:04:05Z"))
	default:
		return nil, errors.New("unsupported FileES mail template")
	}
	plainBody := fmt.Sprintf("%s\r\n\r\n%s\r\n\r\n%s\r\n", intro, code, footer)
	htmlBody := renderMailHTML(intro, code, footer, branding)
	body, contentType, err := buildMailBody(entry.MessageID, plainBody, htmlBody, branding)
	if err != nil {
		return nil, err
	}
	message := fmt.Sprintf(
		"From: FileES <%s>\r\n"+
			"To: <%s>\r\n"+
			"Date: %s\r\n"+
			"Message-ID: <%s@%s>\r\n"+
			"Subject: %s\r\n"+
			"Auto-Submitted: auto-generated\r\n"+
			"MIME-Version: 1.0\r\n"+
			"Content-Type: %s\r\n"+
			"\r\n%s",
		canonicalFrom,
		to,
		entry.CreatedAt.UTC().Format("Mon, 02 Jan 2006 15:04:05 -0700"),
		entry.MessageID,
		strings.ToLower(messageIDDomain),
		subject,
		contentType,
		body,
	)
	return []byte(message), nil
}

func CanonicalEmail(value string) (string, error) { return canonicalEmail(value) }

// buildMailBody assembles the text/plain + text/html alternative, plus (when
// branding carries a logo) an outer multipart/related wrapping it with the
// logo as an inline, Content-ID-referenced part. A CID part is used instead
// of a data: URI because several widely deployed mail clients (notably
// Outlook's desktop Word rendering engine) block or mis-render data: image
// sources in HTML mail even though the same clients handle inline CID
// attachments correctly.
//
// Boundaries are derived deterministically from the message ID rather than
// left to multipart.Writer's crypto/rand default: RenderMail must produce
// byte-identical output for the same job on every delivery retry (see
// TestRenderMailIsStableAndHeaderSafe), and a random boundary would break
// that on every call.
func buildMailBody(messageID, plainBody, htmlBody string, branding realmbranding.Branding) (body, contentType string, err error) {
	altBuffer := &bytes.Buffer{}
	altWriter := multipart.NewWriter(altBuffer)
	if err := altWriter.SetBoundary(mailBoundary(messageID, "alt")); err != nil {
		return "", "", err
	}
	if err := writeMailTextPart(altWriter, "text/plain; charset=utf-8", plainBody); err != nil {
		return "", "", err
	}
	if err := writeMailTextPart(altWriter, "text/html; charset=utf-8", htmlBody); err != nil {
		return "", "", err
	}
	if err := altWriter.Close(); err != nil {
		return "", "", err
	}
	if branding.LogoBase64 == "" {
		return altBuffer.String(), "multipart/alternative; boundary=" + altWriter.Boundary(), nil
	}
	logoRaw, err := base64.StdEncoding.DecodeString(branding.LogoBase64)
	if err != nil {
		return "", "", fmt.Errorf("mail branding logo: %w", err)
	}
	relatedBuffer := &bytes.Buffer{}
	relatedWriter := multipart.NewWriter(relatedBuffer)
	if err := relatedWriter.SetBoundary(mailBoundary(messageID, "rel")); err != nil {
		return "", "", err
	}
	altPart, err := relatedWriter.CreatePart(textproto.MIMEHeader{
		"Content-Type": {"multipart/alternative; boundary=" + altWriter.Boundary()},
	})
	if err != nil {
		return "", "", err
	}
	if _, err := altPart.Write(altBuffer.Bytes()); err != nil {
		return "", "", err
	}
	imagePart, err := relatedWriter.CreatePart(textproto.MIMEHeader{
		"Content-Type":              {branding.LogoMediaType},
		"Content-Transfer-Encoding": {"base64"},
		"Content-ID":                {"<" + logoContentID + ">"},
		"Content-Disposition":       {"inline"},
	})
	if err != nil {
		return "", "", err
	}
	if _, err := imagePart.Write([]byte(wrapMailBase64(logoRaw))); err != nil {
		return "", "", err
	}
	if err := relatedWriter.Close(); err != nil {
		return "", "", err
	}
	return relatedBuffer.String(), "multipart/related; boundary=" + relatedWriter.Boundary(), nil
}

// writeMailTextPart declares 7bit rather than transforming the body, because
// every value that reaches it (fixed English copy, an OTP, a base64url
// invitation token, an RFC3339-ish timestamp) is guaranteed plain ASCII -
// there is nothing here that quoted-printable or base64 would need to escape.
func writeMailTextPart(writer *multipart.Writer, contentType, text string) error {
	part, err := writer.CreatePart(textproto.MIMEHeader{
		"Content-Type":              {contentType},
		"Content-Transfer-Encoding": {"7bit"},
	})
	if err != nil {
		return err
	}
	_, err = part.Write([]byte(text))
	return err
}

// mailBoundary derives a MIME boundary from the message ID instead of using
// multipart.Writer's random default, hex-encoded so it stays within RFC
// 2046's boundary charset regardless of what the message ID itself contains.
func mailBoundary(messageID, part string) string {
	sum := sha256.Sum256([]byte(part + ":" + messageID))
	return "filees-" + hex.EncodeToString(sum[:12])
}

func wrapMailBase64(raw []byte) string {
	encoded := base64.StdEncoding.EncodeToString(raw)
	var wrapped strings.Builder
	for i := 0; i < len(encoded); i += 76 {
		end := min(i+76, len(encoded))
		wrapped.WriteString(encoded[i:end])
		wrapped.WriteString("\r\n")
	}
	return wrapped.String()
}

// renderMailHTML is deliberately table-based with only inline style=""
// attributes - no <style> block, no CSS classes, no shadows/gradients/custom
// fonts. Those are the constructs most likely to be stripped or mangled by
// mail clients (Outlook's Word engine chief among them); a plain nested
// table with inline styles is the one layout approach that renders the same
// almost everywhere.
func renderMailHTML(intro, code, footer string, branding realmbranding.Branding) string {
	accent := branding.LeadingColor
	logoImg := ""
	if branding.LogoBase64 != "" {
		logoImg = `<img src="cid:` + logoContentID + `" width="32" height="32" alt="FileES" style="display:block;border:0;">`
	}
	var b strings.Builder
	b.WriteString("<!doctype html>\r\n")
	b.WriteString(`<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>FileES</title></head>` + "\r\n")
	b.WriteString(`<body style="margin:0;padding:0;background-color:#F4F5F7;">` + "\r\n")
	b.WriteString(`<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="background-color:#F4F5F7;"><tr><td align="center" style="padding:32px 16px;">` + "\r\n")
	b.WriteString(`<table role="presentation" width="480" cellpadding="0" cellspacing="0" style="max-width:480px;width:100%;background-color:#FFFFFF;border-radius:8px;">` + "\r\n")
	b.WriteString(`<tr><td style="background-color:` + accent + `;padding:20px 24px;border-radius:8px 8px 0 0;">` + logoImg + `</td></tr>` + "\r\n")
	b.WriteString(`<tr><td style="padding:32px 24px;font-family:Arial,Helvetica,sans-serif;color:#0B1D3A;">` + "\r\n")
	b.WriteString(`<p style="margin:0 0 16px 0;font-size:15px;line-height:1.5;">` + html.EscapeString(intro) + `</p>` + "\r\n")
	b.WriteString(`<p style="margin:0 0 16px 0;font-size:22px;font-weight:bold;letter-spacing:1px;text-align:center;word-break:break-all;font-family:'Courier New',Courier,monospace;color:` + accent + `;">` + html.EscapeString(code) + `</p>` + "\r\n")
	b.WriteString(`<p style="margin:0;font-size:13px;line-height:1.5;color:#5B6472;">` + html.EscapeString(footer) + `</p>` + "\r\n")
	b.WriteString(`</td></tr></table>` + "\r\n")
	b.WriteString(`</td></tr></table>` + "\r\n")
	b.WriteString(`</body></html>`)
	return b.String()
}
