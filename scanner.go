package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Item struct {
	Path         string    `json:"path"`
	Size         int64     `json:"size"`
	Language     string    `json:"language"`
	Selected     bool      `json:"-"`
	LastModified time.Time `json:"last_modified"`
}

type rule struct {
	DirName string
	Markers []string
	Lang    string
}

var rules = []rule{
	// Node ecosystem
	{"node_modules", nil, "Node.js"},
	{".next", nil, "Node.js"},
	{".nuxt", nil, "Node.js"},
	{".turbo", nil, "Node.js"},
	{".parcel-cache", nil, "Node.js"},
	{".yarn", nil, "Node.js"},

	// .NET (markers as extensions)
	{"bin", []string{".csproj", ".fsproj", ".vbproj", ".sln"}, ".NET"},
	{"obj", []string{".csproj", ".fsproj", ".vbproj", ".sln"}, ".NET"},

	// Rust
	{"target", []string{"Cargo.toml"}, "Rust"},

	// Java/Maven
	{"target", []string{"pom.xml"}, "Java (Maven)"},

	// Java/Gradle
	{"build", []string{"build.gradle", "build.gradle.kts", "settings.gradle", "settings.gradle.kts"}, "Java (Gradle)"},
	{".gradle", nil, "Java (Gradle)"},

	// Python
	{"__pycache__", nil, "Python"},
	{".venv", nil, "Python"},
	{"venv", nil, "Python"},
	{".pytest_cache", nil, "Python"},
	{".mypy_cache", nil, "Python"},
	{".ruff_cache", nil, "Python"},
	{".tox", nil, "Python"},

	// Go
	{"vendor", []string{"go.mod"}, "Go"},

	// PHP
	{"vendor", []string{"composer.json"}, "PHP"},

	// Ruby
	{".bundle", nil, "Ruby"},

	// Elixir
	{"_build", []string{"mix.exs"}, "Elixir"},
	{"deps", []string{"mix.exs"}, "Elixir"},

	// Swift/Xcode
	{"DerivedData", nil, "Swift/Xcode"},
	{".swiftpm", nil, "Swift/Xcode"},
	{".build", []string{"Package.swift"}, "Swift"},

	// Dart/Flutter
	{".dart_tool", nil, "Dart/Flutter"},

	// C/C++
	{"cmake-build-debug", nil, "C/C++"},
	{"cmake-build-release", nil, "C/C++"},
}

func markerMatches(filename, marker string) bool {
	if strings.HasPrefix(marker, ".") && strings.Count(marker, ".") == 1 {
		return strings.HasSuffix(filename, marker)
	}
	return filename == marker
}

// matchRuleWithEntries matches a candidate dir against rules, using the
// already-loaded sibling entries (avoids a redundant ReadDir).
func matchRuleWithEntries(name string, siblings []fs.DirEntry) (string, bool) {
	for _, r := range rules {
		if r.DirName != name {
			continue
		}
		if len(r.Markers) == 0 {
			return r.Lang, true
		}
		for _, e := range siblings {
			if e.IsDir() {
				continue
			}
			for _, m := range r.Markers {
				if markerMatches(e.Name(), m) {
					return r.Lang, true
				}
			}
		}
	}
	return "", false
}

func isVCS(name string) bool {
	return name == ".git" || name == ".hg" || name == ".svn"
}

// Scan walks root in parallel, sums each artifact's size in parallel, and
// returns a flat list. The find-walk and per-artifact size walks run
// concurrently, sharing a single semaphore that caps in-flight ReadDir
// syscalls so we don't melt the disk.
func Scan(root string) ([]Item, error) {
	concurrency := runtime.NumCPU() * 4
	if concurrency < 8 {
		concurrency = 8
	}
	sem := make(chan struct{}, concurrency)

	var resultMu sync.Mutex
	var results []Item

	var sizeWG sync.WaitGroup
	sizeOne := func(path, lang string) {
		defer sizeWG.Done()
		size, mtime := dirSizeAndMtime(path, sem)
		abs, err := filepath.Abs(path)
		if err != nil {
			abs = path
		}
		resultMu.Lock()
		results = append(results, Item{
			Path:         abs,
			Size:         size,
			Language:     lang,
			Selected:     true,
			LastModified: mtime,
		})
		resultMu.Unlock()
	}

	var walkWG sync.WaitGroup
	var walk func(dir string)
	walk = func(dir string) {
		defer walkWG.Done()
		sem <- struct{}{}
		entries, err := os.ReadDir(dir)
		<-sem
		if err != nil {
			return
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			name := e.Name()
			if isVCS(name) {
				continue
			}
			sub := filepath.Join(dir, name)
			if lang, ok := matchRuleWithEntries(name, entries); ok {
				sizeWG.Add(1)
				go sizeOne(sub, lang)
				continue
			}
			walkWG.Add(1)
			go walk(sub)
		}
	}

	walkWG.Add(1)
	go walk(root)
	walkWG.Wait()
	sizeWG.Wait()

	return results, nil
}

// dirSizeAndMtime sums all regular file sizes under root and returns the
// most recent ModTime seen across the whole subtree. One goroutine per dir;
// the shared sem caps concurrent ReadDir calls.
func dirSizeAndMtime(root string, sem chan struct{}) (int64, time.Time) {
	var total atomic.Int64
	var maxMtime atomic.Int64

	bumpMtime := func(t time.Time) {
		mt := t.UnixNano()
		for {
			cur := maxMtime.Load()
			if mt <= cur {
				return
			}
			if maxMtime.CompareAndSwap(cur, mt) {
				return
			}
		}
	}

	if info, err := os.Stat(root); err == nil {
		bumpMtime(info.ModTime())
	}

	var wg sync.WaitGroup
	var walk func(dir string)
	walk = func(dir string) {
		defer wg.Done()
		sem <- struct{}{}
		entries, err := os.ReadDir(dir)
		<-sem
		if err != nil {
			return
		}
		for _, e := range entries {
			info, err := e.Info()
			if err != nil {
				continue
			}
			if e.IsDir() {
				bumpMtime(info.ModTime())
				wg.Add(1)
				go walk(filepath.Join(dir, e.Name()))
				continue
			}
			// Don't follow symlinks; size of the link target isn't ours to delete.
			if info.Mode()&os.ModeSymlink != 0 {
				continue
			}
			total.Add(info.Size())
			bumpMtime(info.ModTime())
		}
	}
	wg.Add(1)
	go walk(root)
	wg.Wait()

	var t time.Time
	if mn := maxMtime.Load(); mn > 0 {
		t = time.Unix(0, mn)
	}
	return total.Load(), t
}
