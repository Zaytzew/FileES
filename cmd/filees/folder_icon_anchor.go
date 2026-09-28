package main

import (
	"image"
	"image/color"
	"math"
)

// A distinct ICO, not a shell overlay handler: it consumes no Explorer overlay
// slots and is removed by the normal managed-folder desktop.ini cleanup.
func anchorFolderIconPath() (string, error) {
	return decoratedFolderIconPath("filees-anchor.ico", anchorFolderIconBytes)
}

func repositoryFolderIconPath(anchor bool) (string, error) {
	if anchor {
		return anchorFolderIconPath()
	}
	return managedFolderIconPath()
}

func anchorFolderIconBytes() ([]byte, error) {
	return decoratedFolderIconBytes(drawAnchorBadge)
}

// Supersampled vector strokes keep the same glyph at every embedded ICO size.
// White anchor, navy disk and orange rim reuse FileES colours, without fonts.
func drawAnchorBadge(canvas *image.NRGBA) {
	b := canvas.Bounds()
	r := float64(b.Dx()) * .235
	cx, cy := float64(b.Max.X)-r-.5, float64(b.Max.Y)-r-.5
	segments := [][4]float64{
		{0, -.25, 0, .62}, {-.34, -.05, .34, -.05},
		{0, .62, -.37, .43}, {-.37, .43, -.55, .15},
		{0, .62, .37, .43}, {.37, .43, .55, .15},
		{-.55, .15, -.58, .4}, {-.55, .15, -.31, .23},
		{.55, .15, .58, .4}, {.55, .15, .31, .23},
	}
	for y := int(cy - r); y < b.Max.Y; y++ {
		for x := int(cx - r); x < b.Max.X; x++ {
			base := canvas.NRGBAAt(x, y)
			var red, green, blue, alpha float64
			for sy := 0; sy < 4; sy++ {
				for sx := 0; sx < 4; sx++ {
					px := (float64(x) + (float64(sx)+.5)/4 - cx) / r
					py := (float64(y) + (float64(sy)+.5)/4 - cy) / r
					c := base
					if d := math.Hypot(px, py); d <= 1 {
						c = color.NRGBA{15, 32, 57, 255}
						if d > .9 {
							c = color.NRGBA{255, 113, 16, 255}
						}
						stroke := math.Abs(math.Hypot(px, py+.45)-.18) <= .08
						for _, s := range segments {
							dx, dy := s[2]-s[0], s[3]-s[1]
							t := math.Max(0, math.Min(1, ((px-s[0])*dx+(py-s[1])*dy)/(dx*dx+dy*dy)))
							stroke = stroke || math.Hypot(px-s[0]-t*dx, py-s[1]-t*dy) <= .08
						}
						if stroke {
							c = color.NRGBA{250, 247, 242, 255}
						}
					}
					a := float64(c.A)
					red += float64(c.R) * a
					green += float64(c.G) * a
					blue += float64(c.B) * a
					alpha += a
				}
			}
			if alpha > 0 {
				canvas.SetNRGBA(x, y, color.NRGBA{uint8(red / alpha), uint8(green / alpha), uint8(blue / alpha), uint8(alpha / 16)})
			}
		}
	}
}
