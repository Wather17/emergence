package vault

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"filippo.io/age"
)

type entry struct {
	Name string `json:"name"`
	Dir  bool   `json:"dir,omitempty"`
	Size int64  `json:"size,omitempty"`
	Hash string `json:"sha256,omitempty"`
}

// Tests substitute a lower work factor; production uses age's defaults.
var newRecipient = age.NewScryptRecipient

func digest(path string) (string, int64, error) {
	info, err := plain(path)
	if err != nil {
		return "", 0, err
	}
	if !info.Mode().IsRegular() {
		return "", 0, fmt.Errorf("não é arquivo: %s", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), n, err
}

func snapshot(root string) ([]entry, error) {
	entries := []entry{}
	seen := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, _ os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := plain(path)
		if err != nil {
			return err
		}
		if path == root {
			if !info.IsDir() {
				return fmt.Errorf("esperada uma pasta: %s", root)
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(rel)
		if err := safePath(name); err != nil {
			return err
		}
		key := strings.ToLower(name)
		if seen[key] {
			return fmt.Errorf("nomes diferem apenas por maiúsculas: %s", name)
		}
		seen[key] = true
		e := entry{Name: name, Dir: info.IsDir()}
		if !e.Dir {
			e.Hash, e.Size, err = digest(path)
			if err != nil {
				return err
			}
		}
		entries = append(entries, e)
		return nil
	})
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries, err
}

func encrypt(path, root, password string, entries []entry) (err error) {
	r, err := newRecipient(password)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer func() {
		if e := f.Close(); err == nil {
			err = e
		}
	}()
	w, err := age.Encrypt(f, r)
	if err != nil {
		return err
	}
	tw := tar.NewWriter(w)
	for _, e := range entries {
		h := &tar.Header{Name: e.Name, Mode: 0600, Size: e.Size, Typeflag: tar.TypeReg}
		if e.Dir {
			h.Typeflag = tar.TypeDir
			h.Mode = 0700
		}
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if e.Dir {
			continue
		}
		p := filepath.Join(root, filepath.FromSlash(e.Name))
		if _, err := plain(p); err != nil {
			return err
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		hash := sha256.New()
		n, copyErr := io.Copy(io.MultiWriter(tw, hash), in)
		closeErr := in.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if n != e.Size || hex.EncodeToString(hash.Sum(nil)) != e.Hash {
			return fmt.Errorf("arquivo mudou durante a leitura: %s", e.Name)
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return f.Sync()
}

// destination must be an empty, private staging directory, or empty for verify.
// Drain the age stream after TAR EOF to authenticate even its trailing bytes.
func decrypt(path, password, destination string) ([]entry, error) {
	if _, err := plain(path); err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	id, err := age.NewScryptIdentity(password)
	if err != nil {
		return nil, err
	}
	r, err := age.Decrypt(f, id)
	if err != nil {
		return nil, fmt.Errorf("senha incorreta ou arquivo criptografado inválido: %w", err)
	}
	tr := tar.NewReader(r)
	entries := []entry{}
	seen := map[string]bool{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if err := safePath(h.Name); err != nil {
			return nil, err
		}
		if h.Typeflag != tar.TypeDir && h.Typeflag != tar.TypeReg {
			return nil, fmt.Errorf("tipo de arquivo não permitido: %s", h.Name)
		}
		key := strings.ToLower(h.Name)
		if seen[key] {
			return nil, fmt.Errorf("entrada duplicada: %s", h.Name)
		}
		seen[key] = true
		e := entry{Name: h.Name, Dir: h.Typeflag == tar.TypeDir, Size: h.Size}
		if e.Dir && e.Size != 0 {
			return nil, fmt.Errorf("diretório com conteúdo inválido")
		}
		var out *os.File
		if destination != "" {
			p := filepath.Join(destination, filepath.FromSlash(e.Name))
			if e.Dir {
				if err := os.MkdirAll(p, 0700); err != nil {
					return nil, err
				}
			} else {
				if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
					return nil, err
				}
				out, err = os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
				if err != nil {
					return nil, err
				}
			}
		}
		if !e.Dir {
			hash := sha256.New()
			var dst io.Writer = hash
			if out != nil {
				dst = io.MultiWriter(hash, out)
			}
			n, err := io.Copy(dst, tr)
			if out != nil {
				syncErr := out.Sync()
				closeErr := out.Close()
				if err == nil {
					err = syncErr
				}
				if err == nil {
					err = closeErr
				}
			}
			if err != nil {
				return nil, err
			}
			if n != e.Size {
				return nil, fmt.Errorf("arquivo incompleto: %s", e.Name)
			}
			e.Hash = hex.EncodeToString(hash.Sum(nil))
		}
		entries = append(entries, e)
	}
	if _, err := io.Copy(io.Discard, r); err != nil {
		return nil, fmt.Errorf("falha de integridade: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries, nil
}

func same(a, b []entry) bool { return slices.Equal(a, b) }
