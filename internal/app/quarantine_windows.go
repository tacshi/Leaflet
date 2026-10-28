package app

import "os"

// markDownloaded marks a file as from the internet, as browsers do what
// they download, so SmartScreen and Office check it before it runs.
func markDownloaded(path string) {
	_ = os.WriteFile(path+":Zone.Identifier", []byte("[ZoneTransfer]\r\nZoneId=3\r\n"), 0o600)
}
