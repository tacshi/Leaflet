//go:build ios

package mobile

import "leaflet/internal/icloud"

func useDownloads(p Platform) { icloud.StartDownload = p.StartDownload }
