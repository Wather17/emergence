package main

import (
	"archive/zip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManifest(t *testing.T) {
	hashes := map[string]string{"amd64": strings.Repeat("a", 64), "arm64": strings.Repeat("b", 64)}
	m, err := makeManifest("v1.2.3", "Wather17/emergence", "MIT", hashes)
	if err != nil {
		t.Fatal(err)
	}
	if m.Version != "1.2.3" || m.Bin != "emergence.exe" || len(m.Architecture) != 2 {
		t.Fatalf("bad manifest: %+v", m)
	}
	for arch, key := range map[string]string{"amd64": "64bit", "arm64": "arm64"} {
		a := m.Architecture[key]
		if a.URL != "https://github.com/Wather17/emergence/releases/download/v1.2.3/emergence_1.2.3_windows_"+arch+".zip" || a.Hash != hashes[arch] {
			t.Fatal(a)
		}
	}
	for _, tag := range []string{"1.2.3", "v1", "v01.2.3", "v1.2.3-beta", "v1.2.3;bad"} {
		if _, err := makeManifest(tag, "a/b", "MIT", hashes); err == nil {
			t.Fatal("invalid tag accepted", tag)
		}
	}
	if _, err := makeManifest("v1.2.3", "a/b/c", "MIT", hashes); err == nil {
		t.Fatal("invalid repo accepted")
	}
	delete(hashes, "arm64")
	if _, err := makeManifest("v1.2.3", "a/b", "MIT", hashes); err == nil {
		t.Fatal("missing hash accepted")
	}
}

func TestBundleLayout(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "emergence.exe")
	doc := filepath.Join(dir, "README.md")
	for _, p := range []string{exe, doc} {
		if err := os.WriteFile(p, []byte(filepath.Base(p)), 0600); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(dir, "package.zip")
	if err := bundle(path, []string{exe, doc}); err != nil {
		t.Fatal(err)
	}
	r, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if len(r.File) != 2 {
		t.Fatal("unexpected entries")
	}
	for _, f := range r.File {
		if f.Name != "emergence.exe" && f.Name != "README.md" {
			t.Fatal("wrong extraction path", f.Name)
		}
		r, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != f.Name {
			t.Fatal("changed contents")
		}
	}
}
