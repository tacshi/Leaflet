package guard

import (
	"errors"
	"fmt"
	"log"
	"runtime/debug"

	"leaflet/locales"
)

func Go(fn func()) {
	go func() {
		defer Recover()
		fn()
	}()
}

func Recover() {
	if r := recover(); r != nil {
		log.Printf("panic: %v\n%s", r, debug.Stack())
	}
}

func RecoverAs(err *error) {
	if r := recover(); r != nil {
		log.Printf("panic: %v\n%s", r, debug.Stack())
		*err = errors.New(locales.AppErrorInternal(fmt.Sprint(r)))
	}
}
