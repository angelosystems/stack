package main

// version.go — einheitlicher /version-Vertrag (Release-Pipeline-PRD WP2/D18),
// CLI-Oberfläche für Worker ohne HTTP: `solartown-adapter version [--json]`.
// Build-Stamp via deploy.sh -ldflags; Defaults markieren ungestampfte Builds.

import (
	"encoding/json"
	"fmt"
	"os"
)

// Version deklariert main.go (Alt-Stamp); Sha/BuiltAt vervollständigen den
// 5-Feld-Vertrag.
var (
	Sha     = "unknown"
	BuiltAt = "unknown"
)

const versionService = "solartown-adapter"

// maybeRunVersion fängt `version [--json]` VOR flag.Parse ab (die Adapter sind
// flag-basiert). true = bedient, Aufrufer beendet sich. So kann ein alter
// Binary-Stand niemals versehentlich einen Sync fahren, wenn der Reconciler
// `version --json` ruft — der Vertrag ist der ERSTE Check im Prozess.
func maybeRunVersion() bool {
	if len(os.Args) < 2 || os.Args[1] != "version" {
		return false
	}
	env := os.Getenv("MK_ENV")
	if env == "" {
		env = "prod-mvp"
	}
	v := struct {
		Service string `json:"service"`
		Version string `json:"version"`
		Sha     string `json:"sha"`
		BuiltAt string `json:"built_at"`
		Env     string `json:"env"`
	}{versionService, Version, Sha, BuiltAt, env}
	if len(os.Args) > 2 && os.Args[2] == "--json" {
		json.NewEncoder(os.Stdout).Encode(v)
	} else {
		fmt.Printf("%s %s (sha %s, built %s, env %s)\n", v.Service, v.Version, v.Sha, v.BuiltAt, v.Env)
	}
	return true
}
