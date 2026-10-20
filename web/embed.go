// Package web embeds the built dashboard (web/dist).
//
// DECISION: dist/.gitkeep is committed so `go:embed all:dist` always compiles
// and `go build` / `go test` work without Node. A server built without running
// `just build-web` answers / with a short "not built" message.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var embedded embed.FS

// Dist returns the dashboard files rooted at the build output directory.
func Dist() fs.FS {
	sub, err := fs.Sub(embedded, "dist")
	if err != nil {
		panic(err) // dist is embedded at compile time
	}
	return sub
}
