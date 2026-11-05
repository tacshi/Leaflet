package sync

import "time"

// UploadWaitForTest has a push wait d for an upload, for tests of other
// packages, and returns what undoes it.
func UploadWaitForTest(d time.Duration) (undo func()) {
	old := uploadWait
	uploadWait = d
	return func() { uploadWait = old }
}
