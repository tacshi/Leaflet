package sysproxy

import (
	"net"
	"runtime"
	"strconv"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

// CFNetwork's proxy settings, the ones System Settings › Network sets; the
// dictionary they come in is an NSDictionary too.
var (
	cfOnce sync.Once
	cfOK   bool

	copySettings      func() uintptr
	poolPush, poolPop uintptr

	clsString                                                    objc.ID
	selWithUTF8, selObjectForKey, selIntValue, selUTF8, selCount objc.SEL
	selObjectAtIndex, selRelease                                 objc.SEL
)

func loadCFNetwork() bool {
	cfOnce.Do(func() {
		cfnet, err := purego.Dlopen("/System/Library/Frameworks/CFNetwork.framework/CFNetwork", purego.RTLD_LAZY|purego.RTLD_GLOBAL)
		if err != nil {
			return
		}
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
		if _, err := purego.Dlsym(cfnet, "CFNetworkCopySystemProxySettings"); err != nil {
			return
		}
		purego.RegisterLibFunc(&copySettings, cfnet, "CFNetworkCopySystemProxySettings")
		sel := objc.RegisterName
		selWithUTF8, selObjectForKey, selIntValue, selUTF8 = sel("stringWithUTF8String:"), sel("objectForKey:"), sel("intValue"), sel("UTF8String")
		selCount, selObjectAtIndex, selRelease = sel("count"), sel("objectAtIndex:"), sel("release")
		clsString = objc.ID(objc.GetClass("NSString"))
		cfOK = clsString != 0
	})
	return cfOK
}

func read() settings {
	var s settings
	if !loadCFNetwork() {
		return s
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	pool, _, _ := purego.SyscallN(poolPush)
	defer purego.SyscallN(poolPop, pool)
	dict := objc.ID(copySettings())
	if dict == 0 {
		return s
	}
	defer dict.Send(selRelease)
	get := func(key string) objc.ID {
		ckey := append([]byte(key), 0)
		k := clsString.Send(selWithUTF8, unsafe.Pointer(&ckey[0])) // #nosec G103 -- a C string for NSString, kept alive below.
		runtime.KeepAlive(ckey)
		return dict.Send(selObjectForKey, k)
	}
	num := func(key string) int {
		if v := get(key); v != 0 {
			return int(objc.Send[int32](v, selIntValue))
		}
		return 0
	}
	str := func(v objc.ID) string {
		if v == 0 {
			return ""
		}
		return goString(objc.Send[*byte](v, selUTF8))
	}
	proxy := func(kind string) string {
		host, port := str(get(kind+"Proxy")), num(kind+"Port")
		if num(kind+"Enable") == 0 || host == "" || port == 0 {
			return ""
		}
		return net.JoinHostPort(host, strconv.Itoa(port))
	}
	s.HTTP, s.HTTPS, s.SOCKS = proxy("HTTP"), proxy("HTTPS"), proxy("SOCKS")
	s.Simple = num("ExcludeSimpleHostnames") != 0
	if list := get("ExceptionsList"); list != 0 {
		for i := range objc.Send[uint](list, selCount) {
			if h := bypass(str(list.Send(selObjectAtIndex, i))); h != "" {
				s.Bypass = append(s.Bypass, h)
			}
		}
	}
	return s
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
