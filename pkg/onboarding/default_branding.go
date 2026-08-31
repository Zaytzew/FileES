package onboarding

import (
	_ "embed"

	"filees/pkg/realmbranding"
)

// The filees:space square symbol rasterized from the canonical SVG in
// branded-assets/filees-space-svg-pack/filees-space-symbol-square.svg.
//
//go:embed assets/default-logo.png
var defaultLogoPNG []byte

var defaultBranding = mustDefaultBranding()

func mustDefaultBranding() realmbranding.Branding {
	branding, err := realmbranding.FromBytes(realmbranding.DefaultLeadingColor, "image/png", defaultLogoPNG)
	if err != nil {
		panic("onboarding: embedded default logo failed to normalize: " + err.Error())
	}
	return branding
}

// DefaultBranding is the filees:space mark and product accent color, used
// for onboarding mail (and anywhere else an operator hasn't configured its
// own branding) so the product still looks intentional out of the box.
func DefaultBranding() realmbranding.Branding { return defaultBranding }
