// Leaflet is an app where people and AI co-write living documents.
package main

import (
	"fmt"
	"os"

	"github.com/egoist/mygo"

	"leaflet/internal/app"
	"leaflet/internal/mcp"
	"leaflet/internal/sysproxy"
)

func main() {
	sysproxy.Install()
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
