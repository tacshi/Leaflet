// Leaflet is an app where people and AI co-write living documents.
package main

import (
	"os"

	"net/http"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/netproxy"

	"leaflet/internal/app"
	"leaflet/internal/cli"
)

func main() {
	// The app's clients share http.DefaultTransport: it goes through the
	// proxy set in the system's settings, as the browser does, which an
	// app opened from the Finder or the Start menu has no variable of.
	if t, ok := http.DefaultTransport.(*http.Transport); ok {
		t.Proxy = netproxy.Proxy
	}
	// The command line, `leaflet mcp` among its commands: through the link
	// Settings installs, or with a command. Windows' terminals run
	// cmd/leaflet instead, as this is no console program there.
	name := cli.CommandName(mygo.App.Name())
	if cli.Invoked(os.Args, name) {
		os.Exit(cli.Main(os.Args[1:], cli.Options{
			Name: name, Dir: app.DataDir(), Version: mygo.App.Version(),
			In: os.Stdin, Out: os.Stdout, Err: os.Stderr,
		}))
	}
	app.Run()
}
