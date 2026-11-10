package mobile

import "runtime"

func Version() string {
	defer catch(nil)
	return runtime.Version()
}
