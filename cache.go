package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

type cacheFile struct {
	Root      string    `json:"root"`
	ScannedAt time.Time `json:"scanned_at"`
	Items     []Item    `json:"items"`
}

func cachePath(root string) (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "tidy")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	h := sha256.Sum256([]byte(root))
	name := hex.EncodeToString(h[:8])
	return filepath.Join(dir, name+".json"), nil
}

func saveCache(root string, items []Item, scannedAt time.Time) error {
	p, err := cachePath(root)
	if err != nil {
		return err
	}
	data, err := json.Marshal(cacheFile{Root: root, ScannedAt: scannedAt, Items: items})
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func loadCache(root string) (cacheFile, bool) {
	p, err := cachePath(root)
	if err != nil {
		return cacheFile{}, false
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return cacheFile{}, false
	}
	var cf cacheFile
	if err := json.Unmarshal(data, &cf); err != nil {
		return cacheFile{}, false
	}
	// Defence: cache file's stored root must match what we're scanning.
	if cf.Root != root {
		return cacheFile{}, false
	}
	return cf, true
}
