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
	if t, ok := http.DefaultTransport.(*http.Transport); ok {
		t.Proxy = netproxy.Proxy
	}
	name := cli.CommandName(mygo.App.Name())
	if cli.Invoked(os.Args, name) {
		os.Exit(cli.Main(os.Args[1:], cli.Options{
			Name: name, Dir: app.DataDir(), Version: mygo.App.Version(),
			In: os.Stdin, Out: os.Stdout, Err: os.Stderr,
		}))
	}
	app.Run()
}
