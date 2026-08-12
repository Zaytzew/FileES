// Package realmbranding defines the two owner-controlled presentation values
// inherited by every public share in a realm.
package realmbranding

import (
	"bytes"
	"encoding/base64"
	"errors"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"regexp"
	"strings"
)

const (
	DefaultLeadingColor = "#FF6A00"
	MaxLogoBytes        = 32 << 10
	MaxLogoDimension    = 2048
	MaxLogoPixels       = 4_000_000
)

var colorPattern = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)

// Branding has two conceptual values: LeadingColor and Logo. MediaType and
// Base64 are only the safe wire representation of the latter.
type Branding struct {
	LeadingColor  string `json:"leading_color,omitempty"`
	LogoMediaType string `json:"logo_media_type,omitempty"`
	LogoBase64    string `json:"logo_base64,omitempty"`
}

func Default() Branding { return Branding{LeadingColor: DefaultLeadingColor} }

// Normalize validates and canonicalises a branding value. Empty means the
// FileES default, allowing old realm records to remain valid.
func Normalize(value Branding) (Branding, error) {
	color := strings.TrimSpace(value.LeadingColor)
	if color == "" {
		color = DefaultLeadingColor
	}
	if !colorPattern.MatchString(color) {
		return Branding{}, errors.New("leading color must use #RRGGBB")
	}
	value.LeadingColor = strings.ToUpper(color)
	value.LogoMediaType = strings.ToLower(strings.TrimSpace(value.LogoMediaType))
	value.LogoBase64 = strings.TrimSpace(value.LogoBase64)
	if value.LogoMediaType == "" && value.LogoBase64 == "" {
		return value, nil
	}
	if value.LogoMediaType == "" || value.LogoBase64 == "" {
		return Branding{}, errors.New("logo media type and content must be supplied together")
	}
	if value.LogoMediaType != "image/png" && value.LogoMediaType != "image/jpeg" {
		return Branding{}, errors.New("logo must be PNG or JPEG")
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(value.LogoBase64)
	if err != nil || len(raw) == 0 || len(raw) > MaxLogoBytes {
		return Branding{}, errors.New("logo content is invalid or too large")
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || config.Width < 1 || config.Height < 1 || config.Width > MaxLogoDimension || config.Height > MaxLogoDimension || config.Width*config.Height > MaxLogoPixels {
		return Branding{}, errors.New("logo image dimensions are invalid")
	}
	if (format == "png") != (value.LogoMediaType == "image/png") || (format == "jpeg") != (value.LogoMediaType == "image/jpeg") {
		return Branding{}, errors.New("logo media type does not match its content")
	}
	value.LogoBase64 = base64.StdEncoding.EncodeToString(raw)
	return value, nil
}

func FromBytes(leadingColor, mediaType string, logo []byte) (Branding, error) {
	value := Branding{LeadingColor: leadingColor, LogoMediaType: mediaType}
	if len(logo) > 0 {
		value.LogoBase64 = base64.StdEncoding.EncodeToString(logo)
	}
	return Normalize(value)
}
