package main

import (
	"fmt"
	"image/color"
	"time"

	"github.com/getlantern/systray"
)

// updateTrayStatusOnline updates tray with online status
func updateTrayStatusOnline(remoteAlias, displayHost string) {
	iconData := loadIconData(color.RGBA{0, 255, 0, 255})
	if iconData != nil {
		systray.SetIcon(iconData)
	}

	// Check if displayHost is a chain (contains " -> ")
	if remoteAlias == "Chain" {
		systray.SetTitle(fmt.Sprintf("Chain: %s", displayHost))
		systray.SetTooltip(fmt.Sprintf("Chain: %s", displayHost))
	} else {
		systray.SetTitle(fmt.Sprintf("Online -> %s", remoteAlias))
		systray.SetTooltip(fmt.Sprintf("%s: ONLINE\n%s", remoteAlias, displayHost))
	}
}

// updateTrayStatusFailover updates tray with failover status (connected to backup host).
// Shows green icon with "(Failover)" indicator.
func updateTrayStatusFailover(remoteAlias, displayHost string) {
	iconData := loadIconData(color.RGBA{0, 255, 0, 255})
	if iconData != nil {
		systray.SetIcon(iconData)
	}

	systray.SetTitle(fmt.Sprintf("Online -> %s (Failover)", remoteAlias))
	systray.SetTooltip(fmt.Sprintf("%s: FAILOVER\n%s\nWaiting for original chain to recover...", remoteAlias, displayHost))
}

// updateTrayStatusReconnecting updates tray with reconnecting status
func updateTrayStatusReconnecting(remoteAlias, displayHost string) {
	iconData := loadIconData(color.RGBA{255, 255, 0, 255})
	if iconData != nil {
		systray.SetIcon(iconData)
	}

	if remoteAlias == "Chain" {
		systray.SetTitle("Chain: Reconnecting...")
		systray.SetTooltip(fmt.Sprintf("Chain: %s\nStatus: RECONNECTING", displayHost))
	} else {
		systray.SetTitle("Reconnecting...")
		systray.SetTooltip(fmt.Sprintf("%s: RECONNECTING\n%s", remoteAlias, displayHost))
	}
}

// updateTrayStatusWaiting updates tray with waiting status
func updateTrayStatusWaiting(remoteAlias, displayHost, message string) {
	iconData := loadIconData(color.RGBA{255, 255, 0, 255})
	if iconData != nil {
		systray.SetIcon(iconData)
	}

	if remoteAlias == "Chain" {
		systray.SetTitle(fmt.Sprintf("Chain: %s", message))
		systray.SetTooltip(fmt.Sprintf("Chain: %s\nStatus: WAITING", displayHost))
	} else {
		systray.SetTitle(message)
		systray.SetTooltip(fmt.Sprintf("%s: WAITING\n%s", remoteAlias, displayHost))
	}
}

// updateTrayStatusNoInternet updates tray with no internet status
func updateTrayStatusNoInternet(remoteAlias, displayHost string, elapsed, maxTime time.Duration) {
	iconData := loadIconData(color.RGBA{255, 255, 0, 255})
	if iconData != nil {
		systray.SetIcon(iconData)
	}
	remaining := maxTime - elapsed
	minutes := int(remaining.Minutes())
	if minutes < 0 {
		minutes = 0
	}

	if remoteAlias == "Chain" {
		systray.SetTitle(fmt.Sprintf("Chain: No Internet (%dm left)", minutes))
		systray.SetTooltip(fmt.Sprintf("Chain: %s\nStatus: NO INTERNET", displayHost))
	} else {
		systray.SetTitle(fmt.Sprintf("No Internet (%dm left)", minutes))
		systray.SetTooltip(fmt.Sprintf("%s: NO INTERNET\n%s", remoteAlias, displayHost))
	}
}

// updateTrayStatusAttempting updates tray with attempting reconnection status
func updateTrayStatusAttempting(remoteAlias, displayHost string, attempt int, remainingTime time.Duration) {
	iconData := loadIconData(color.RGBA{255, 255, 0, 255})
	if iconData != nil {
		systray.SetIcon(iconData)
	}
	minutes := int(remainingTime.Minutes())
	if minutes < 0 {
		minutes = 0
	}

	if remoteAlias == "Chain" {
		systray.SetTitle(fmt.Sprintf("Chain: Attempt %d (%dm left)", attempt, minutes))
		systray.SetTooltip(fmt.Sprintf("Chain: %s\nStatus: ATTEMPTING RECONNECTION", displayHost))
	} else {
		systray.SetTitle(fmt.Sprintf("Attempt %d (%dm left)", attempt, minutes))
		systray.SetTooltip(fmt.Sprintf("%s: ATTEMPTING RECONNECTION\n%s", remoteAlias, displayHost))
	}
}

// loadIconData loads embedded icon data based on color
func loadIconData(c color.RGBA) []byte {
	iconData := getEmbeddedIcon(c)
	if len(iconData) == 0 {
		// Return empty icon as fallback
		return []byte{}
	}
	return iconData
}
