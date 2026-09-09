package vault

import (
	"archive/tar"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"filippo.io/age"
)

const backupVersion = 1

type backupManifest struct {
	Version int      `json:"version"`
	Files   []string `json:"files"`
}

func ensureOutputPath(root, notes, meta, output string) (string, error) {
	if output == "" {
		return "", errors.New("informe um arquivo de saída com --output")
	}
	abs, err := filepath.Abs(output)
	if err != nil {
		return "", err
	}
	if exists(abs) {
		return "", fmt.Errorf("o destino já existe: %s", abs)
	}
	parent := filepath.Dir(abs)
	if _, err := checkDirectory(parent); err != nil {
		return "", fmt.Errorf("diretório de saída inválido: %w", err)
	}
	for _, target := range []string{meta, notes, filepath.Join(root, ".emergence-init"), filepath.Join(root, ".emergence-restore")} {
		rel, err := filepath.Rel(target, abs)
		if err != nil {
			return "", err
		}
		if rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
			return "", errors.New("o destino não pode ficar dentro de um alvo administrado pela vault")
		}
	}
	return abs, nil
}

func writeBundleFile(tw *tar.Writer, name, source string) error {
	info, err := checkRegular(source)
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: info.Size(), Typeflag: tar.TypeReg}); err != nil {
		return err
	}
	f, err := os.Open(source)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(tw, f)
	closeErr := f.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

// createBundleWithPassword writes a versioned encrypted TAR directly to a
// private temporary file and atomically publishes it at destination.
func createBundleWithPassword(destination, meta, password string, files []string) (err error) {
	parent := filepath.Dir(destination)
	tmp, err := os.CreateTemp(parent, ".emergence-backup-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return err
	}
	recipient, err := newRecipient(password)
	if err != nil {
		_ = tmp.Close()
		return err
	}
	writer, err := age.Encrypt(tmp, recipient)
	if err != nil {
		_ = tmp.Close()
		return err
	}
	tw := tar.NewWriter(writer)
	manifest := backupManifest{Version: backupVersion, Files: append([]string(nil), files...)}
	sort.Strings(manifest.Files)
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tw.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0600, Size: int64(len(manifestBytes)), Typeflag: tar.TypeReg}); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tw.Write(manifestBytes); err != nil {
		_ = tmp.Close()
		return err
	}
	for _, name := range manifest.Files {
		if err := writeBundleFile(tw, name, filepath.Join(meta, filepath.FromSlash(name))); err != nil {
			_ = tmp.Close()
			return err
		}
	}
	if err := tw.Close(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := writer.Close(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, destination); err != nil {
		return err
	}
	if err := syncDir(filepath.Dir(destination)); err != nil {
		return err
	}
	return nil
}

// Backup creates one encrypted, versioned bundle from a locked vault.
func (v *Vault) Backup(output, password string) error {
	if password == "" {
		return errors.New("a senha não pode ser vazia")
	}
	if exists(v.notes()) {
		info, err := plain(v.notes())
		if err != nil {
			return err
		}
		if info.IsDir() {
			return errors.New("a vault está aberta; execute lock antes de criar o backup")
		}
		return errors.New("a pasta privada está em conflito")
	}
	for _, name := range []string{"txn", "prepare", "cleanup"} {
		if exists(v.meta(name)) {
			return errors.New("há uma operação incompleta; recupere-a antes de criar o backup")
		}
	}
	if err := v.ValidatePassword(password); err != nil {
		return err
	}
	files := []string{"config.json", "sealed.age"}
	if exists(v.meta("trash.age")) {
		if _, err := checkRegular(v.meta("trash.age")); err != nil {
			return fmt.Errorf("trash.age inválido: %w", err)
		}
		if _, _, err := readTrashArchiveAll(v.meta("trash.age"), password); err != nil {
			return fmt.Errorf("trash.age inválido: %w", err)
		}
		files = append(files, "trash.age")
	}
	destination, err := ensureOutputPath(v.Root, v.notes(), v.meta(), output)
	if err != nil {
		return err
	}
	return createBundleWithPassword(destination, v.meta(), password, files)
}

func readBackupManifest(stage string, entries []entry) (backupManifest, error) {
	allowed := map[string]bool{"manifest.json": true, "config.json": true, "sealed.age": true, "trash.age": true}
	seen := map[string]bool{}
	for _, e := range entries {
		if e.Dir || !allowed[e.Name] || seen[strings.ToLower(e.Name)] {
			return backupManifest{}, errors.New("bundle contém entradas inválidas")
		}
		seen[strings.ToLower(e.Name)] = true
	}
	if !seen["manifest.json"] || !seen["config.json"] || !seen["sealed.age"] {
		return backupManifest{}, errors.New("bundle não contém os arquivos obrigatórios")
	}
	b, err := os.ReadFile(filepath.Join(stage, "manifest.json"))
	if err != nil {
		return backupManifest{}, err
	}
	var manifest backupManifest
	if err := json.Unmarshal(b, &manifest); err != nil {
		return backupManifest{}, errors.New("manifesto do bundle inválido")
	}
	if manifest.Version != backupVersion {
		return backupManifest{}, errors.New("versão de bundle não suportada")
	}
	if len(manifest.Files) == 0 {
		return backupManifest{}, errors.New("manifesto do bundle não lista arquivos")
	}
	manifestSeen := map[string]bool{}
	for _, name := range manifest.Files {
		if name != "config.json" && name != "sealed.age" && name != "trash.age" {
			return backupManifest{}, errors.New("manifesto do bundle lista um arquivo não permitido")
		}
		key := strings.ToLower(name)
		if manifestSeen[key] || !seen[key] {
			return backupManifest{}, errors.New("manifesto do bundle não corresponde às entradas")
		}
		manifestSeen[key] = true
	}
	if !manifestSeen["config.json"] || !manifestSeen["sealed.age"] || manifestSeen["trash.age"] != seen["trash.age"] {
		return backupManifest{}, errors.New("manifesto do bundle não corresponde às entradas")
	}
	sort.Strings(manifest.Files)
	return manifest, nil
}

// Restore decrypts and validates a bundle in private staging before
// publishing .emergence. Existing ordinary files in the vault root are kept.
func Restore(root, input, password string) error {
	if password == "" {
		return errors.New("a senha não pode ser vazia")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	if _, err := checkDirectory(root); err != nil {
		return err
	}
	input, err = filepath.Abs(input)
	if err != nil {
		return err
	}
	if _, err := checkRegular(input); err != nil {
		return fmt.Errorf("arquivo de backup inválido: %w", err)
	}
	meta := filepath.Join(root, metadata)
	stage := filepath.Join(root, ".emergence-restore")
	for _, path := range []string{meta, filepath.Join(root, ".emergence-init"), stage, filepath.Join(root, ".emergence-destroy")} {
		if exists(path) {
			return fmt.Errorf("o destino já contém estado administrado: %s", path)
		}
	}
	if err := os.Mkdir(stage, 0700); err != nil {
		return err
	}
	published := false
	defer func() {
		if !published {
			_ = os.RemoveAll(stage)
		}
	}()
	entries, err := decrypt(input, password, stage)
	if err != nil {
		return fmt.Errorf("backup inválido ou senha incorreta: %w", err)
	}
	if _, err := readBackupManifest(stage, entries); err != nil {
		return err
	}
	configPath := filepath.Join(stage, "config.json")
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}
	var c config
	if err := json.Unmarshal(configBytes, &c); err != nil {
		return errors.New("configuração do bundle inválida")
	}
	if c.Version != 1 || validFolder(c.Folder) != nil || validInbox(c.Inbox) != nil {
		return errors.New("configuração do bundle inválida")
	}
	if _, err := decrypt(filepath.Join(stage, "sealed.age"), password, ""); err != nil {
		return fmt.Errorf("sealed.age do bundle é inválido: %w", err)
	}
	if exists(filepath.Join(stage, "trash.age")) {
		if _, _, err := readTrashArchiveAll(filepath.Join(stage, "trash.age"), password); err != nil {
			return fmt.Errorf("trash.age do bundle é inválido: %w", err)
		}
	}
	notesPath := filepath.Join(root, c.Folder)
	if exists(notesPath) {
		return fmt.Errorf("a pasta privada já existe: %s", notesPath)
	}
	entriesRoot, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, e := range entriesRoot {
		if strings.EqualFold(e.Name(), c.Folder) || strings.EqualFold(e.Name(), metadata) || strings.EqualFold(e.Name(), ".emergence-init") {
			return fmt.Errorf("colisão no destino: %s", e.Name())
		}
	}
	if c.Inbox != "" {
		inboxPath := filepath.Join(root, filepath.FromSlash(c.Inbox))
		rel, err := filepath.Rel(notesPath, inboxPath)
		if err != nil || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
			return errors.New("a Inbox do bundle não pode ficar dentro da pasta privada")
		}
	}
	if err := move(stage, meta); err != nil {
		return err
	}
	published = true
	return nil
}
