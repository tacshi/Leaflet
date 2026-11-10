package main

import (
	"cmp"
	"os"
	"path/filepath"

	"leaflet/internal/cli"
)

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
