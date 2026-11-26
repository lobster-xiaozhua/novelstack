// Package adminui 内嵌 Vue 3 控制台的构建产物。
// 源工程在 /web；`make web` 会重新构建并覆盖 dist。
package adminui

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var files embed.FS

// FS 返回以 dist 为根的只读文件系统。
func FS() (fs.FS, error) {
	return fs.Sub(files, "dist")
}
