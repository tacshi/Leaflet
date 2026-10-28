// Leaflet is an app where people and AI co-write living documents.
package main

import (
	"fmt"
	"os"

	"net/http"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/netproxy"

	"leaflet/internal/app"
	"leaflet/internal/mcp"
)

func main() {
	// The app's clients share http.DefaultTransport: it goes through the
	// proxy set in the system's settings, as the browser does, which an
	// app opened from the Finder or the Start menu has no variable of.
	if t, ok := http.DefaultTransport.(*http.Transport); ok {
		t.Proxy = netproxy.Proxy
	}
	// `leaflet mcp` serves the pages to agents outside the app, on stdio.
	if len(os.Args) == 2 && os.Args[1] == "mcp" {
		if err := mcp.Main(app.DataDir(), mygo.App.Version(), os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "leaflet mcp:", err)
			os.Exit(1)
		}
		return
	}
	app.Run()
}
