// Package ui ships a built version of the sql-studio frontend embedded into the
// binary. The built assets in ./dist are committed; consumers embed the
// frontend by importing this package and passing FS() to the server.
package ui

import (
	"embed"
	"io/fs"
)

// Version identifies the frontend build shipped by this package.
const Version = "0.1.51"

// dist holds the built frontend assets (the contents of the React app's
// production build). `make assets` copies ui/dist into ./dist.
//
//go:embed all:dist
var dist embed.FS

// FS returns the built frontend asset filesystem, rooted at the dist directory
// (so "index.html", "assets/...", etc. are at the root of the returned FS).
func FS() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		// The embed directive guarantees dist exists; this is unreachable.
		panic(err)
	}
	return sub
}
