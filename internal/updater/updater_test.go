package updater

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"net/http"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// makeTarGz 生成含 trae-relay 二进制占位的 tar.gz。
func makeTarGz(t *testing.T, binName string) []byte {
	t.Helper()
	var buf strings.Builder
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	content := "#!/bin/sh\necho fake-binary-v2\n" + strings.Repeat("x", 1024)
	_ = tw.WriteHeader(&tar.Header{Name: binName, Mode: 0o755, Size: int64(len(content))})
	_, _ = tw.Write([]byte(content))
	_ = tw.Close()
	_ = gz.Close()
	return []byte(buf.String())
}

// TestCheckAndDownload 用假 GitHub API 验证 Check 匹配资产 + download 解压。
func TestCheckAndDownload(t *testing.T) {
	binName := "trae-relay"
	assetName := binName + "-" + runtime.GOOS + "-" + runtime.GOARCH + ".tar.gz"
	payload := makeTarGz(t, binName)

	mux := http.NewServeMux()
	mux.HandleFunc("/repos/test/trae-relay/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		_ = jsonWrite(w, map[string]any{
			"tag_name": "v9.9.9", "name": "test release", "body": "notes",
			"assets": []map[string]any{{
				"name": assetName, "size": len(payload),
				"browser_download_url": "http://" + r.Host + "/dl/" + assetName,
			}},
		})
	})
	mux.HandleFunc("/dl/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	dir := t.TempDir()
	u := New("test/trae-relay", dir, binName, "v0.0.1")
	u.APIBase = srv.URL

	rel, err := u.Check(context.Background())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if rel.Tag != "v9.9.9" || !rel.Update || rel.Asset != assetName {
		t.Fatalf("rel = %+v", rel)
	}

	dest := filepath.Join(dir, "out.bin")
	if err := u.download(context.Background(), rel.URL, dest); err != nil {
		t.Fatalf("download: %v", err)
	}
	raw, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "fake-binary-v2") {
		t.Fatalf("extracted content wrong: %.60s", raw)
	}
}

func jsonWrite(w http.ResponseWriter, v any) error {
	w.Header().Set("Content-Type", "application/json")
	b, _ := json.Marshal(v)
	_, err := w.Write(b)
	return err
}
