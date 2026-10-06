// Package admin 管理 API 与内嵌 Web 控制台。
package admin

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
)

//go:embed index.html assets
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
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	_, _ = w.Write(raw)
}

// Asset 输出内嵌静态资源（打赏码等），路径白名单校验防目录穿越。
func Asset(w http.ResponseWriter, r *http.Request) {
	name := path.Base(r.PathValue("name"))
	raw, err := fs.ReadFile(indexHTML, "assets/"+name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ct := "application/octet-stream"
	switch {
	case stringsHasSuffix(name, ".jpg"), stringsHasSuffix(name, ".jpeg"):
		ct = "image/jpeg"
	case stringsHasSuffix(name, ".png"):
		ct = "image/png"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(raw)
}

func stringsHasSuffix(s, suf string) bool {
	return len(s) >= len(suf) && s[len(s)-len(suf):] == suf
}
