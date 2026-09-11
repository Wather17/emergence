package vault

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Use the same portable name rules on both systems so a vault can be moved.
// Callers must validate archive-controlled names before joining them to a
// filesystem destination.
func safePath(name string) error {
	if name == "" || strings.ContainsAny(name, "\\:\x00") || strings.HasPrefix(name, "/") {
		return fmt.Errorf("caminho inválido: %q", name)
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." || strings.TrimRight(part, " .") != part || strings.ContainsAny(part, "<>\"|?*") {
			return fmt.Errorf("nome não portátil: %q", name)
		}
		if strings.EqualFold(part, ".emergence") {
			return fmt.Errorf("nome reservado pelo Emergence: %q", name)
		}
		for _, r := range part {
			if r < 32 {
				return fmt.Errorf("nome não portátil: %q", name)
			}
		}
		base := strings.ToUpper(strings.SplitN(part, ".", 2)[0])
		runes := []rune(base)
		if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" || base == "CONIN$" || base == "CONOUT$" || (len(runes) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && strings.ContainsRune("123456789¹²³", runes[3])) {
			return fmt.Errorf("nome reservado no Windows: %q", name)
		}
	}
	return nil
}

func plain(path string) (os.FileInfo, error) {
	i, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if i.Mode()&os.ModeSymlink != 0 || (!i.IsDir() && !i.Mode().IsRegular()) {
		return nil, fmt.Errorf("links e arquivos especiais não são aceitos: %s", path)
	}
	if err := rejectReparse(path); err != nil {
		return nil, err
	}
	return i, nil
}

func exists(path string) bool { _, err := os.Lstat(path); return !os.IsNotExist(err) }

func validateTree(path string) error {
	return filepath.WalkDir(path, func(p string, _ os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		_, err = plain(p)
		return err
	})
}
