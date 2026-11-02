//go:build !ios

package mobile

// useDownloads leaves iCloud's downloads to the icloud package, off iOS,
// where the facade runs only in tests.
func useDownloads(Platform) {}
