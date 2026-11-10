package app

import (
	"testing"

	"github.com/egoist/mygo"
)

func TestICloudWatcherFollowsTheDeviceID(t *testing.T) {
	a := templatesApp(t)
	old := a.st.DeviceID()
	own := &icloudOwn{st: a.st}
	own.read()
	if !own.ignores("devices/"+old, true) || !own.ignores("devices/"+old+"/log/000005.json", false) || !own.ignores("blobs/ab", false) ||
		own.ignores("devices/other", true) || own.ignores("devices/"+old+"x", true) {
		t.Fatal("ignores the wrong folders")
	}
	if err := a.st.SetValue("device.id", "renamed"); err != nil {
		t.Fatal(err)
	}
	if own.othersWrote([]mygo.FileEvent{{Op: mygo.FileHint, Path: "devices/renamed/log"}, {Op: mygo.FileHint, Path: "blobs"}}) {
		t.Error("this device's writes under its new ID woke a pass")
	}
	if own.ignores("devices/"+old, true) || !own.othersWrote([]mygo.FileEvent{{Op: mygo.FileHint, Path: "devices/" + old}}) {
		t.Error("another device's writes under the old ID are ignored")
	}
	if !own.othersWrote([]mygo.FileEvent{{Op: mygo.FileHint, Path: "."}}) {
		t.Error("changes the system lost woke no pass")
	}
}
