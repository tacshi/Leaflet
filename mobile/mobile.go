// Package mobile is the part of Leaflet the iPhone and iPad app calls,
// bound to Swift with gomobile (make ios-core). It takes and returns only
// what gomobile carries across: strings, bytes, numbers, booleans, errors
// and interfaces, with structured values as JSON.
//
// It must never import MyGo's windowing packages: CI checks that its
// dependencies for iOS leave them out.
package mobile

import "runtime"

// Version returns the Go release the framework was built with, which the
// app logs at launch to show the framework linked and answers.
func Version() string {
	return runtime.Version()
}
