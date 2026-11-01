//go:build ios

package mobile

import "leaflet/internal/icloud"

// useDownloads has iCloud's downloads asked for through the app.
func useDownloads(p Platform) { icloud.StartDownload = p.StartDownload }
