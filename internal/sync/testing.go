package sync

import "time"

func UploadWaitForTest(d time.Duration) (undo func()) {
	old := uploadWait
	uploadWait = d
	return func() { uploadWait = old }
}
