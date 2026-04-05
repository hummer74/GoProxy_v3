package main

import (
	_ "embed"
	"image/color"
)

//go:embed icon_green.ico
var iconGreen []byte

//go:embed icon_yellow.ico
var iconYellow []byte

//go:embed icon_red.ico
var iconRed []byte

// getEmbeddedIcon returns embedded icon by color
func getEmbeddedIcon(c color.RGBA) []byte {
	switch {
	case c.R == 0 && c.G == 255 && c.B == 0: // Green
		if len(iconGreen) > 0 {
			return iconGreen
		}
	case c.R == 255 && c.G == 255 && c.B == 0: // Yellow
		if len(iconYellow) > 0 {
			return iconYellow
		}
	case c.R == 255 && c.G == 0 && c.B == 0: // Red
		if len(iconRed) > 0 {
			return iconRed
		}
	}
	// Return yellow as default if specific color not found
	if len(iconYellow) > 0 {
		return iconYellow
	}
	return nil // No icons available
}
