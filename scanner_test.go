package main

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScan_PrunesAndDetectsLanguages(t *testing.T) {
	root := t.TempDir()

	// Node project with nested node_modules — the nested one must NOT be reported.
	writeFile(t, filepath.Join(root, "web", "package.json"), "{}")
	writeFile(t, filepath.Join(root, "web", "node_modules", "lib", "index.js"), "x")
	writeFile(t, filepath.Join(root, "web", "node_modules", "lib", "node_modules", "nested", "f.js"), "y")

	// Rust project.
	writeFile(t, filepath.Join(root, "rs", "Cargo.toml"), "[package]")
	writeFile(t, filepath.Join(root, "rs", "target", "debug", "x"), "z")

	// .NET project — bin/obj should be detected only because of .csproj sibling.
	writeFile(t, filepath.Join(root, "dotnet", "App.csproj"), "<Project/>")
	writeFile(t, filepath.Join(root, "dotnet", "bin", "Debug", "App.dll"), "a")
	writeFile(t, filepath.Join(root, "dotnet", "obj", "stuff"), "b")

	// 'bin' without a .csproj should NOT be picked up.
	writeFile(t, filepath.Join(root, "scripts", "bin", "tool"), "c")

	// .git should be skipped entirely.
	writeFile(t, filepath.Join(root, ".git", "objects", "x"), "d")

	items, err := Scan(root)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}

	got := map[string]string{}
	for _, it := range items {
		rel, _ := filepath.Rel(root, it.Path)
		got[rel] = it.Language
	}

	want := map[string]string{
		filepath.Join("web", "node_modules"): "Node.js",
		filepath.Join("rs", "target"):        "Rust",
		filepath.Join("dotnet", "bin"):       ".NET",
		filepath.Join("dotnet", "obj"):       ".NET",
	}

	if len(got) != len(want) {
		t.Errorf("expected %d items, got %d: %v", len(want), len(got), got)
	}
	for path, lang := range want {
		if got[path] != lang {
			t.Errorf("missing or wrong language for %s: got %q want %q", path, got[path], lang)
		}
	}
	if _, bad := got[filepath.Join("scripts", "bin")]; bad {
		t.Errorf("scripts/bin should not be reported as deletable")
	}
	for k := range got {
		if filepath.Base(filepath.Dir(k)) == "node_modules" {
			t.Errorf("nested node_modules leaked into results: %s", k)
		}
	}
}
