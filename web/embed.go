// Package web menyertakan hasil build SPA (web/dist) ke binary Go.
// Jalankan `npm --prefix web run build` setelah mengubah kode di web/src.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Dist berisi index.html dan folder assets hasil build Vite.
func Dist() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}
