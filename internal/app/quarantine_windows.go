package app

import "os"

// markDownloaded marks a file as from the internet, as browsers do what
// they download, so SmartScreen and Office check it before it runs. A
// file marked already keeps what it has, as the address a browser that
// downloaded it noted.
func markDownloaded(path string) {
	// Made only where there is none: a mark is a stream of the file's.
	f, err := os.OpenFile(path+":Zone.Identifier", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- the mark of a file of the app's own.
	if err != nil {
		return
	}
	_, _ = f.WriteString("[ZoneTransfer]\r\nZoneId=3\r\n")
	_ = f.Close()
}
