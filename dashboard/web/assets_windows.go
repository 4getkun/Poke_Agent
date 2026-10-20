//go:build windows

package web

import (
	"embed"
	"io/fs"
)

//go:embed dist-windows/windows.html dist-windows/assets
var built embed.FS

func Assets() fs.FS {
	assets, err := fs.Sub(built, "dist-windows")
	if err != nil {
		panic(err)
	}
	return assets
}
