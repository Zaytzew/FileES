// Package realmbranding defines the two owner-controlled presentation values
// inherited by every public share in a realm.
package realmbranding

import (
	"bytes"
	"encoding/base64"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math"
	"regexp"
	"strings"
)

const (
	DefaultLeadingColor   = "#FF6A00"
	MaxLogoBytes          = 32 << 10
	MaxLogoInputBytes     = 16 << 20
	MaxLogoDimension      = 2048
	MaxLogoPixels         = 4_000_000
	MaxLogoInputDimension = 8192
	MaxLogoInputPixels    = 32_000_000
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

// PrepareLogo accepts an ordinary owner-supplied image and prepares the small
// web asset that can safely travel inside the bounded control ticket. Scaling
// is proportional; PNG transparency is preserved and JPEG input remains JPEG.
func PrepareLogo(leadingColor, mediaType string, raw []byte) (Branding, error) {
	if len(raw) == 0 || len(raw) > MaxLogoInputBytes {
		return Branding{}, errors.New("logo source is empty or larger than 16 MiB")
	}
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	if mediaType != "image/png" && mediaType != "image/jpeg" {
		return Branding{}, errors.New("logo must be PNG or JPEG")
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || config.Width < 1 || config.Height < 1 || config.Width > MaxLogoInputDimension || config.Height > MaxLogoInputDimension || config.Width*config.Height > MaxLogoInputPixels {
		return Branding{}, errors.New("logo source dimensions are invalid")
	}
	if (format == "png") != (mediaType == "image/png") || (format == "jpeg") != (mediaType == "image/jpeg") {
		return Branding{}, errors.New("logo media type does not match its content")
	}
	if len(raw) <= MaxLogoBytes && config.Width <= MaxLogoDimension && config.Height <= MaxLogoDimension {
		return FromBytes(leadingColor, mediaType, raw)
	}
	source, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return Branding{}, errors.New("logo image cannot be decoded")
	}
	for edge := 512; edge >= 96; edge = edge * 3 / 4 {
		width, height := fitLogoSize(config.Width, config.Height, edge)
		resized := resizeLogoBilinear(source, width, height)
		qualities := []int{88}
		if mediaType == "image/jpeg" {
			qualities = []int{88, 78, 68}
		}
		for _, quality := range qualities {
			var encoded bytes.Buffer
			if mediaType == "image/png" {
				err = (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(&encoded, resized)
			} else {
				err = jpeg.Encode(&encoded, resized, &jpeg.Options{Quality: quality})
			}
			if err == nil && encoded.Len() <= MaxLogoBytes {
				return FromBytes(leadingColor, mediaType, encoded.Bytes())
			}
		}
	}
	return Branding{}, errors.New("logo cannot be reduced to the safe web size")
}

func fitLogoSize(width, height, maxEdge int) (int, int) {
	if width <= maxEdge && height <= maxEdge {
		return width, height
	}
	if width >= height {
		return maxEdge, max(1, height*maxEdge/width)
	}
	return max(1, width*maxEdge/height), maxEdge
}

func resizeLogoBilinear(source image.Image, width, height int) *image.NRGBA {
	bounds := source.Bounds()
	target := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		fy := (float64(y)+0.5)*float64(bounds.Dy())/float64(height) - 0.5
		y0 := int(math.Floor(fy))
		wy := fy - float64(y0)
		if y0 < 0 {
			y0, wy = 0, 0
		}
		y1 := min(y0+1, bounds.Dy()-1)
		for x := 0; x < width; x++ {
			fx := (float64(x)+0.5)*float64(bounds.Dx())/float64(width) - 0.5
			x0 := int(math.Floor(fx))
			wx := fx - float64(x0)
			if x0 < 0 {
				x0, wx = 0, 0
			}
			x1 := min(x0+1, bounds.Dx()-1)
			r00, g00, b00, a00 := source.At(bounds.Min.X+x0, bounds.Min.Y+y0).RGBA()
			r10, g10, b10, a10 := source.At(bounds.Min.X+x1, bounds.Min.Y+y0).RGBA()
			r01, g01, b01, a01 := source.At(bounds.Min.X+x0, bounds.Min.Y+y1).RGBA()
			r11, g11, b11, a11 := source.At(bounds.Min.X+x1, bounds.Min.Y+y1).RGBA()
			r := bilinearChannel(r00, r10, r01, r11, wx, wy)
			g := bilinearChannel(g00, g10, g01, g11, wx, wy)
			b := bilinearChannel(b00, b10, b01, b11, wx, wy)
			a := bilinearChannel(a00, a10, a01, a11, wx, wy)
			pixel := color.NRGBA{A: uint8(a / 257)}
			if a > 0 {
				pixel.R = uint8(min(uint32(65535), r*65535/a) / 257)
				pixel.G = uint8(min(uint32(65535), g*65535/a) / 257)
				pixel.B = uint8(min(uint32(65535), b*65535/a) / 257)
			}
			target.SetNRGBA(x, y, pixel)
		}
	}
	return target
}

func bilinearChannel(c00, c10, c01, c11 uint32, wx, wy float64) uint32 {
	top := float64(c00)*(1-wx) + float64(c10)*wx
	bottom := float64(c01)*(1-wx) + float64(c11)*wx
	return uint32(math.Round(top*(1-wy) + bottom*wy))
}
