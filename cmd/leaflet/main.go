// Command leaflet is the command line on Windows (internal/cli), which
// ships beside the app as bin\leaflet.exe: the app is a GUI program
// there, which a terminal neither waits for nor shows the output of. It
// links the pages and their tools without the app, so it is a small
// program of its own, built by scripts/lib/release.sh's build_cli.
package main

import (
	"cmp"
	"os"
	"path/filepath"

	"leaflet/internal/cli"
)

// appName and version are the app's, which the build gives: the data
// folder is named after the app, as MyGo names it (mygo.PathUserData).
var (
	appName = "Leaflet"
	version = ""
)

func main() {
	name := cli.CommandName(appName)
	os.Exit(cli.Main(os.Args[1:], cli.Options{
		Name: name, Dir: dataDir(), Version: version,
		In: os.Stdin, Out: os.Stdout, Err: os.Stderr,
	}))
}

// dataDir is the app's data folder, as app.DataDir has it.
func dataDir() string {
	if d := os.Getenv("LEAFLET_DATA_DIR"); d != "" {
		return d
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, ".leaflet")
	}
	return filepath.Join(dir, cmp.Or(appName, "Leaflet"))
}
