package transport

import (
	"os"
	"path/filepath"
)

// resolveWebUIDir 把 PI_WEBUI_DIR 解析成绝对路径。
//
// 文档约定该变量写成「相对 pi-bridge-go 目录」的路径（例如 ../pi-webui-htmx），
// 但 `go test` 里每个包**以包目录为 cwd** 运行，直接按 cwd 解析会得到
// internal/pi-webui-htmx 这种不存在的路径——文档命令照抄就会失败，
// 报错还只是「目录不存在」，很难联想到 cwd。
//
// 所以相对路径一律按「从 cwd 向上找到含 go.mod 的模块根」为基准解析。
// 向上找不到时原样返回，让调用方按自己的方式报错。
func resolveWebUIDir(dir string) string {
	if dir == "" || filepath.IsAbs(dir) {
		return dir
	}
	wd, err := os.Getwd()
	if err != nil {
		return dir
	}
	for d := wd; ; {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return filepath.Join(d, dir)
		}
		parent := filepath.Dir(d)
		if parent == d {
			return dir
		}
		d = parent
	}
}
