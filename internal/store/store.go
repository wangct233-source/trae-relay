// Package store 提供带锁的 JSON 文件持久化，写入为「临时文件 + rename」原子替换。
package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// JSONFile 一个泛型 JSON 持久化文件。
type JSONFile[T any] struct {
	path string
	mu   sync.RWMutex
	data T
}

// NewJSONFile 打开（或创建目录）一个 JSON 持久化文件并载入现有内容。
func NewJSONFile[T any](dir, name string, zero T) (*JSONFile[T], error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	f := &JSONFile[T]{path: filepath.Join(dir, name), data: zero}
	raw, err := os.ReadFile(f.path)
	if err != nil {
		if os.IsNotExist(err) {
			return f, nil
		}
		return nil, err
	}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &f.data) // 损坏文件回退到 zero 值
	}
	return f, nil
}

// Get 返回当前值的深拷贝快照（通过 JSON 序列化保证隔离）。
func (f *JSONFile[T]) Get() T {
	f.mu.RLock()
	defer f.mu.RUnlock()
	raw, _ := json.Marshal(f.data)
	var out T
	_ = json.Unmarshal(raw, &out)
	return out
}

// Update 在锁内执行 mutate 并原子落盘。
func (f *JSONFile[T]) Update(mutate func(*T)) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	mutate(&f.data)
	return f.flushLocked()
}

// flushLocked 序列化并原子写入（同目录临时文件 + rename）。
func (f *JSONFile[T]) flushLocked() error {
	raw, err := json.MarshalIndent(f.data, "", "  ")
	if err != nil {
		return err
	}
	tmp := f.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, f.path)
}
