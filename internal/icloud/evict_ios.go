//go:build ios

package icloud

// StartDownload asks iCloud to download the file at path, named as it will
// be once downloaded. The iOS app sets it, to FileManager's
// startDownloadingUbiquitousItem(at:), before the first sync; without it
// a file not downloaded yet waits for iCloud to bring it.
var StartDownload func(path string) error
