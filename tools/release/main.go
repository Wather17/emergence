// Command release builds the exact Windows packages used by GitHub and Scoop.
// It is also runnable on Linux to validate packaging without publishing.
package main

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var stableTag = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
var repositoryName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9][A-Za-z0-9_.-]*$`)
var commitID = regexp.MustCompile(`^[a-f0-9]{7,40}$`)

type architecture struct {
	URL  string `json:"url"`
	Hash string `json:"hash"`
}

type manifest struct {
	Version      string                  `json:"version"`
	Description  string                  `json:"description"`
	Homepage     string                  `json:"homepage"`
	License      string                  `json:"license"`
	Architecture map[string]architecture `json:"architecture"`
	Bin          string                  `json:"bin"`
	Checkver     string                  `json:"checkver"`
}

func makeManifest(tag, repository, license string, hashes map[string]string) (manifest, error) {
	if !stableTag.MatchString(tag) {
		return manifest{}, fmt.Errorf("expected stable tag vX.Y.Z, got %q", tag)
	}
	if !repositoryName.MatchString(repository) {
		return manifest{}, errors.New("expected repository owner/name")
	}
	m := manifest{Version: strings.TrimPrefix(tag, "v"), Description: "Encrypted private notes for a local Obsidian vault.", Homepage: "https://github.com/" + repository, License: license, Bin: "emergence.exe", Checkver: "github", Architecture: map[string]architecture{}}
	for _, arch := range []string{"amd64", "arm64"} {
		hash := hashes[arch]
		if !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(hash) {
			return manifest{}, fmt.Errorf("missing SHA-256 for %s", arch)
		}
		key := arch
		if arch == "amd64" {
			key = "64bit"
		}
		m.Architecture[key] = architecture{URL: m.Homepage + "/releases/download/" + tag + "/" + assetName(tag, arch), Hash: hash}
	}
	return m, nil
}

func assetName(tag, arch string) string {
	return "emergence_" + strings.TrimPrefix(tag, "v") + "_windows_" + arch + ".zip"
}

func bundle(path string, files []string) (err error) {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() {
		if e := f.Close(); err == nil {
			err = e
		}
	}()
	w := zip.NewWriter(f)
	for _, path := range files {
		in, err := os.Open(path)
		if err != nil {
			_ = w.Close()
			return err
		}
		h := &zip.FileHeader{Name: filepath.Base(path), Method: zip.Deflate, Modified: time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)}
		h.SetMode(0644)
		if strings.HasSuffix(path, ".exe") {
			h.SetMode(0755)
		}
		out, err := w.CreateHeader(h)
		if err == nil {
			_, err = io.Copy(out, in)
		}
		closeErr := in.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			_ = w.Close()
			return err
		}
	}
	return w.Close()
}

// Include the Go runtime and dependency license texts with redistributed bins.
func notices() ([]byte, error) {
	cmd := exec.Command("go", "list", "-deps", "-json", "./cmd/emergence")
	cmd.Env = append(os.Environ(), "GOOS=windows", "GOARCH=amd64", "CGO_ENABLED=0")
	data, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var result strings.Builder
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	seen := map[string]bool{}
	for {
		var pkg struct {
			Module *struct {
				Path, Version, Dir string
				Main               bool
			}
		}
		if err := decoder.Decode(&pkg); err == io.EOF {
			break
		} else if err != nil {
			return nil, err
		}
		mod := pkg.Module
		if mod == nil || mod.Main || seen[mod.Path] {
			continue
		}
		seen[mod.Path] = true
		license, err := os.ReadFile(filepath.Join(mod.Dir, "LICENSE"))
		if err != nil {
			return nil, fmt.Errorf("license for %s (run go mod download): %w", mod.Path, err)
		}
		fmt.Fprintf(&result, "%s %s\n\n%s\n\n", mod.Path, mod.Version, license)
	}
	root, err := exec.Command("go", "env", "GOROOT").Output()
	if err != nil {
		return nil, err
	}
	license, err := os.ReadFile(filepath.Join(strings.TrimSpace(string(root)), "LICENSE"))
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(&result, "Go runtime and standard library\n\n%s\n", license)
	return []byte(result.String()), nil
}

func run(tag, repository, commit, dist string) error {
	// Validate before creating files or invoking builds.
	dummy := strings.Repeat("0", 64)
	if _, err := makeManifest(tag, repository, "Unknown", map[string]string{"amd64": dummy, "arm64": dummy}); err != nil {
		return err
	}
	if !commitID.MatchString(commit) {
		return errors.New("expected hexadecimal commit ID")
	}
	if err := os.MkdirAll(dist, 0755); err != nil {
		return err
	}
	text, err := notices()
	if err != nil {
		return err
	}
	noticePath := filepath.Join(dist, "THIRD-PARTY-NOTICES.txt")
	if err := os.WriteFile(noticePath, text, 0644); err != nil {
		return err
	}
	licenseName := "Unknown"
	licenseFiles := []string{}
	if b, err := os.ReadFile("LICENSE"); err == nil {
		licenseFiles = append(licenseFiles, "LICENSE")
		if strings.HasPrefix(string(b), "MIT License") {
			licenseName = "MIT"
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	hashes := map[string]string{}
	var checksums strings.Builder
	for _, arch := range []string{"amd64", "arm64"} {
		dir := filepath.Join(dist, arch)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
		exe := filepath.Join(dir, "emergence.exe")
		cmd := exec.Command("go", "build", "-trimpath", "-ldflags", "-s -w -X main.version="+strings.TrimPrefix(tag, "v")+" -X main.commit="+commit, "-o", exe, "./cmd/emergence")
		cmd.Env = append(os.Environ(), "GOOS=windows", "GOARCH="+arch, "CGO_ENABLED=0")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return err
		}
		name := assetName(tag, arch)
		path := filepath.Join(dist, name)
		files := append([]string{exe, "README.md", noticePath}, licenseFiles...)
		if err := bundle(path, files); err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		h := sha256.New()
		_, err = io.Copy(h, f)
		closeErr := f.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		hashes[arch] = fmt.Sprintf("%x", h.Sum(nil))
		fmt.Fprintf(&checksums, "%s  %s\n", hashes[arch], name)
	}
	m, err := makeManifest(tag, repository, licenseName, hashes)
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dist, "emergence.json"), append(b, '\n'), 0644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dist, "checksums.txt"), []byte(checksums.String()), 0644)
}

func main() {
	tag := flag.String("tag", "", "stable release tag vX.Y.Z")
	repo := flag.String("repository", "", "GitHub owner/repository")
	commit := flag.String("commit", "", "source commit")
	dist := flag.String("dist", "dist", "output directory")
	flag.Parse()
	if err := run(*tag, *repo, *commit, *dist); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
