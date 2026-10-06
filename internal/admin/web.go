// Package admin 管理 API 与内嵌 Web 控制台。
package admin

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed index.html
var indexHTML embed.FS

// Page 输出控制台单页。
func Page(w http.ResponseWriter, r *http.Request) {
	raw, err := fs.ReadFile(indexHTML, "index.html")
	if err != nil {
		w.WriteHeader(500)
		_, _ = w.Write([]byte("console page missing"))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(raw)
}
