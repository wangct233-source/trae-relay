// Package updater 热更新：从 GitHub Releases 拉取预编译二进制，自替换并重启进程。
//
// 流程：Check（查最新 Release）→ Apply（下载 tar.gz → 解压校验 →
// 原子替换 BIN_DIR 下二进制 → syscall.Exec 以新二进制重启当前进程）。
// Docker 部署时把 BIN_DIR 挂载为卷，容器重启后仍是新版本。
package updater

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Release GitHub Release 摘要。
type Release struct {
	Tag     string   `json:"tag"`
	Name    string   `json:"name"`
	Notes   string   `json:"notes"`
	Asset   string   `json:"asset"`   // 匹配当前平台的资产名
	Size    int64    `json:"size"`
	URL     string   `json:"url"`
	Current string   `json:"current"` // 本进程版本
	Update  bool     `json:"update"`  // 是否可更新
}

// Updater 热更新器。
type Updater struct {
	Repo    string // GitHub owner/repo
	BinDir  string
	BinName string
	Current string // 当前版本号（构建注入）
	APIBase string // GitHub API 地址（测试可覆盖）

	HTTP *http.Client

	mu       sync.Mutex
	lastTag  string
	lastErr  string
	lastChk  time.Time
	applying bool
	restart  bool
}

func New(repo, binDir, binName, current string) *Updater {
	return &Updater{
		Repo: repo, BinDir: binDir, BinName: binName, Current: current,
		APIBase: "https://api.github.com",
		HTTP:    &http.Client{Timeout: 30 * time.Second},
	}
}

// Status 供控制台轮询。
func (u *Updater) Status() map[string]any {
	u.mu.Lock()
	defer u.mu.Unlock()
	return map[string]any{
		"current": u.Current, "latest": u.lastTag,
		"last_check": u.lastChk.Format("2006-01-02 15:04:05"),
		"error":      u.lastErr,
		"applying":   u.applying,
		"restarting": u.restart,
		"repo":       u.Repo,
	}
}

// Check 查询最新 Release 并匹配当前平台资产。
func (u *Updater) Check(ctx context.Context) (*Release, error) {
	u.mu.Lock()
	u.lastChk = time.Now()
	u.mu.Unlock()
	if u.Repo == "" {
		return nil, fmt.Errorf("未配置 UPDATE_REPO（GitHub owner/repo）")
	}
	url := fmt.Sprintf("%s/repos/%s/releases/latest", strings.TrimRight(u.APIBase, "/"), u.Repo)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := u.HTTP.Do(req)
	if err != nil {
		u.fail(err)
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		err := fmt.Errorf("GitHub API [%d]", resp.StatusCode)
		u.fail(err)
		return nil, err
	}
	var rel struct {
		TagName string `json:"tag_name"`
		Name    string `json:"name"`
		Body    string `json:"body"`
		Assets  []struct {
			Name string `json:"name"`
			Size int64  `json:"size"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		u.fail(err)
		return nil, err
	}
	want := fmt.Sprintf("%s-%s-%s.tar.gz", u.BinName, runtime.GOOS, runtime.GOARCH)
	out := &Release{Tag: rel.TagName, Name: rel.Name, Notes: rel.Body, Current: u.Current}
	for _, a := range rel.Assets {
		if a.Name == want {
			out.Asset, out.Size, out.URL = a.Name, a.Size, a.URL
			break
		}
	}
	out.Update = out.Tag != "" && normalizeTag(out.Tag) != normalizeTag(u.Current)
	u.mu.Lock()
	u.lastTag = out.Tag
	u.lastErr = ""
	u.mu.Unlock()
	return out, nil
}

func (u *Updater) fail(err error) {
	u.mu.Lock()
	u.lastErr = err.Error()
	u.mu.Unlock()
}

func normalizeTag(s string) string {
	if len(s) > 1 && (s[0] == 'v' || s[0] == 'V') {
		return s[1:]
	}
	return s
}

// Apply 下载并替换二进制后以新版本重启进程（version 为空取最新）。
func (u *Updater) Apply(ctx context.Context, version string) (*Release, error) {
	u.mu.Lock()
	if u.applying {
		u.mu.Unlock()
		return nil, fmt.Errorf("更新正在进行中")
	}
	u.applying = true
	u.mu.Unlock()
	defer func() {
		u.mu.Lock()
		u.applying = false
		u.mu.Unlock()
	}()

	rel, err := u.Check(ctx)
	if err != nil {
		return nil, err
	}
	if version != "" {
		// 指定版本：重新匹配该 tag 的资产
		rel, err = u.checkTag(ctx, version)
		if err != nil {
			return nil, err
		}
	} else if !rel.Update {
		return rel, fmt.Errorf("已是最新版本 %s", u.Current)
	}
	if rel.URL == "" {
		return rel, fmt.Errorf("Release %s 未包含当前平台资产（%s/%s）", rel.Tag, runtime.GOOS, runtime.GOARCH)
	}

	newBin := filepath.Join(u.BinDir, u.BinName+".new")
	if err := u.download(ctx, rel.URL, newBin); err != nil {
		u.fail(err)
		return rel, fmt.Errorf("下载失败: %w", err)
	}
	if err := os.Chmod(newBin, 0o755); err != nil {
		return rel, err
	}

	curBin := filepath.Join(u.BinDir, u.BinName)
	oldBin := curBin + ".old"
	_ = os.Remove(oldBin)
	if err := os.Rename(curBin, oldBin); err != nil && !os.IsNotExist(err) {
		return rel, fmt.Errorf("备份旧二进制失败: %w", err)
	}
	if err := os.Rename(newBin, curBin); err != nil {
		_ = os.Rename(oldBin, curBin) // 回滚
		return rel, fmt.Errorf("替换二进制失败: %w", err)
	}
	// 标记来源为热更新：容器 entrypoint 据此不覆盖卷内二进制
	_ = os.WriteFile(filepath.Join(u.BinDir, ".trae_source"), []byte("hotupdate"), 0o644)
	log.Printf("[update] 已更新到 %s，准备重启进程", rel.Tag)
	u.mu.Lock()
	u.restart = true
	u.mu.Unlock()

	// 以新二进制原位重启（保持监听端口短暂的 systemd/容器级中断窗口）
	execPath, _ := os.Executable()
	go func() {
		time.Sleep(500 * time.Millisecond) // 让 HTTP 响应先送达
		_ = syscall.Exec(execPath, os.Args, os.Environ())
		// Exec 失败则直接退出，交给容器/supervisor 拉起
		os.Exit(0)
	}()
	return rel, nil
}

// Run 自动更新循环（autoUpdate 由控制台开关控制）。
func (u *Updater) Run(ctx context.Context, interval time.Duration, autoUpdate func() bool) {
	for {
		if autoUpdate() && u.Repo != "" {
			cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
			if rel, err := u.Check(cctx); err == nil && rel.Update {
				log.Printf("[update] 发现新版本 %s，自动更新", rel.Tag)
				if _, err := u.Apply(cctx, rel.Tag); err != nil {
					log.Printf("[update] 自动更新失败: %v", err)
				}
			}
			cancel()
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

func (u *Updater) checkTag(ctx context.Context, tag string) (*Release, error) {
	url := fmt.Sprintf("%s/repos/%s/releases/tags/%s", strings.TrimRight(u.APIBase, "/"), u.Repo, tag)
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := u.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GitHub API [%d]", resp.StatusCode)
	}
	var rel struct {
		TagName string `json:"tag_name"`
		Name    string `json:"name"`
		Body    string `json:"body"`
		Assets  []struct {
			Name string `json:"name"`
			Size int64  `json:"size"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, err
	}
	want := fmt.Sprintf("%s-%s-%s.tar.gz", u.BinName, runtime.GOOS, runtime.GOARCH)
	out := &Release{Tag: rel.TagName, Name: rel.Name, Notes: rel.Body, Current: u.Current}
	for _, a := range rel.Assets {
		if a.Name == want {
			out.Asset, out.Size, out.URL = a.Name, a.Size, a.URL
			break
		}
	}
	return out, nil
}

// download 下载 tar.gz 并解压出二进制到 dest。
func (u *Updater) download(ctx context.Context, url, dest string) error {
	hc := &http.Client{Timeout: 15 * time.Minute}
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("下载 [%d]", resp.StatusCode)
	}
	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return fmt.Errorf("不是有效的 gzip 包: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("压缩包内未找到二进制 %s", u.BinName)
		}
		if err != nil {
			return err
		}
		if filepath.Base(hdr.Name) == u.BinName && hdr.Typeflag == tar.TypeReg {
			f, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, io.LimitReader(tr, 1<<30)); err != nil {
				f.Close()
				return err
			}
			return f.Close()
		}
	}
}
