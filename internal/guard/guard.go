// Package guard keeps a panic on a goroutine of Leaflet's own from taking
// the app down with it. On iOS each call into the core catches its own
// panics, but nothing catches one on a goroutine the core started, and
// the app would quit with the person's edits.
package guard

import (
	"errors"
	"fmt"
	"log"
	"runtime/debug"

	"leaflet/locales"
)

// Go runs fn on a goroutine of its own, as Recover guards it.
func Go(fn func()) {
	go func() {
		defer Recover()
		fn()
	}()
}

// Recover, deferred first in a goroutine, logs a panic in it, with where
// it happened, and ends the goroutine instead of the app.
func Recover() {
	if r := recover(); r != nil {
		log.Printf("panic: %v\n%s", r, debug.Stack())
	}
}

// RecoverAs is Recover for a goroutine that says how it went in *err: the
// panic becomes that error, in the app's language, as the phone's calls
// into the core word theirs, so it is not taken for success.
func RecoverAs(err *error) {
	if r := recover(); r != nil {
		log.Printf("panic: %v\n%s", r, debug.Stack())
		*err = errors.New(locales.AppErrorInternal(fmt.Sprint(r)))
	}
}
