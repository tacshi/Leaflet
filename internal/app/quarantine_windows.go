package app

import "os"

func markDownloaded(path string) {
	f, err := os.OpenFile(path+":Zone.Identifier", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304
	if err != nil {
		return
	}
	_, _ = f.WriteString("[ZoneTransfer]\r\nZoneId=3\r\n")
	_ = f.Close()
}
