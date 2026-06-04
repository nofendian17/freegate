// Package web embeds the freegate dashboard assets into the Go binary.
package web

import (
	"embed"
	"io/fs"
	"mime"
)

func init() {
	// Ensure MIME types are known on systems that lack a system mime DB.
	mime.AddExtensionType(".css", "text/css; charset=utf-8")
	mime.AddExtensionType(".js", "application/javascript; charset=utf-8")
}

//go:embed all:templates all:static
var assets embed.FS

// Templates returns the embedded template FS rooted at "templates".
func Templates() fs.FS {
	sub, _ := fs.Sub(assets, "templates")
	return sub
}

// Static returns the embedded static FS rooted at "static".
func Static() fs.FS {
	sub, _ := fs.Sub(assets, "static")
	return sub
}
