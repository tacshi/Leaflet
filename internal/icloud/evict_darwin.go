package icloud

import (
	"errors"
	"io/fs"
	"runtime"
	"sync"
	"syscall"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

// sfDataless is the flag of a file whose content iCloud has not
// downloaded (macOS 14 and later list such files under their own names;
// reading one waits for the download).
const sfDataless = 0x40000000

func notReady(info fs.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Flags&sfDataless != 0
}

// Foundation's NSFileManager asks iCloud to download a file.
var (
	nsOnce sync.Once
	nsOK   bool

	poolPush, poolPop uintptr

	clsFileManager, clsURL, clsString                                      objc.ID
	selDefault, selFileURL, selWithUTF8, selDownload, selDescribe, selUTF8 objc.SEL
)

func loadFoundation() bool {
	nsOnce.Do(func() {
		if _, err := purego.Dlopen("/System/Library/Frameworks/Foundation.framework/Foundation", purego.RTLD_LAZY|purego.RTLD_GLOBAL); err != nil {
			return
		}
		libobjc, err := purego.Dlopen("/usr/lib/libobjc.A.dylib", purego.RTLD_LAZY|purego.RTLD_GLOBAL)
		if err != nil {
			return
		}
		if poolPush, err = purego.Dlsym(libobjc, "objc_autoreleasePoolPush"); err != nil {
			return
		}
		if poolPop, err = purego.Dlsym(libobjc, "objc_autoreleasePoolPop"); err != nil {
			return
		}
		sel := objc.RegisterName
		selDefault, selFileURL, selWithUTF8 = sel("defaultManager"), sel("fileURLWithPath:"), sel("stringWithUTF8String:")
		selDownload, selDescribe, selUTF8 = sel("startDownloadingUbiquitousItemAtURL:error:"), sel("localizedDescription"), sel("UTF8String")
		clsFileManager, clsURL, clsString = objc.ID(objc.GetClass("NSFileManager")), objc.ID(objc.GetClass("NSURL")), objc.ID(objc.GetClass("NSString"))
		nsOK = clsFileManager != 0 && clsURL != 0 && clsString != 0
	})
	return nsOK
}

// startDownload asks iCloud to download the file at path, named as it
// will be once downloaded.
var startDownload = func(path string) error {
	if !loadFoundation() {
		return errors.New("the Foundation framework is not available")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	pool, _, _ := purego.SyscallN(poolPush)
	defer purego.SyscallN(poolPop, pool)
	cpath := append([]byte(path), 0)
	str := clsString.Send(selWithUTF8, unsafe.Pointer(&cpath[0])) // #nosec G103 -- a C string for NSString, kept alive below.
	runtime.KeepAlive(cpath)
	url := clsURL.Send(selFileURL, str)
	var nserr objc.ID
	if objc.Send[bool](clsFileManager.Send(selDefault), selDownload, url, unsafe.Pointer(&nserr)) { // #nosec G103 -- the NSError out-parameter.
		return nil
	}
	if nserr == 0 {
		return errors.New("iCloud did not start the download")
	}
	return errors.New(goString(objc.Send[*byte](nserr.Send(selDescribe), selUTF8)))
}

// goString copies a C string.
func goString(p *byte) string {
	if p == nil {
		return ""
	}
	var b []byte
	for i := 0; ; i++ {
		c := *(*byte)(unsafe.Add(unsafe.Pointer(p), i)) // #nosec G103 -- reading the C string NSString returned.
		if c == 0 {
			break
		}
		b = append(b, c)
	}
	return string(b)
}
