package vault

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"filippo.io/age"
)

const trashVersion = 1

// TrashEntry is metadata for one deleted note. Its content is never included
// in list output; the bytes remain encrypted inside trash.age.
type TrashEntry struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Hash      string    `json:"sha256"`
	Size      int64     `json:"size"`
	DeletedAt time.Time `json:"deleted_at"`
}

type trashManifest struct {
	Version int          `json:"version"`
	Entries []TrashEntry `json:"entries"`
}

func trashPath(v *Vault) string { return v.meta("trash.age") }

func trashEntryID(file reviewFile, now time.Time) string {
	h := sha256.Sum256([]byte(file.Name + "\x00" + file.Hash + "\x00" + now.UTC().Format(time.RFC3339Nano)))
	return hex.EncodeToString(h[:])
}

func validateTrashEntry(entry TrashEntry) error {
	if len(entry.ID) != 64 || strings.Trim(entry.ID, "0123456789abcdef") != "" {
		return errors.New("id de quarentena inválido")
	}
	if err := safePath(entry.Name); err != nil {
		return err
	}
	if entry.Size < 0 || len(entry.Hash) != 64 || strings.Trim(entry.Hash, "0123456789abcdef") != "" {
		return errors.New("metadados de quarentena inválidos")
	}
	return nil
}

func readTrashArchiveAll(path, password string) (trashManifest, map[string][]byte, error) {
	if _, err := checkRegular(path); err != nil {
		return trashManifest{}, nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return trashManifest{}, nil, err
	}
	defer f.Close()
	identity, err := age.NewScryptIdentity(password)
	if err != nil {
		return trashManifest{}, nil, err
	}
	r, err := age.Decrypt(f, identity)
	if err != nil {
		return trashManifest{}, nil, fmt.Errorf("senha incorreta ou quarentena inválida: %w", err)
	}
	tr := tar.NewReader(r)
	h, err := tr.Next()
	if err != nil {
		return trashManifest{}, nil, errors.New("quarentena sem manifesto")
	}
	if h.Name != "manifest.json" || h.Typeflag != tar.TypeReg || h.Size < 0 || h.Size > 1<<20 {
		return trashManifest{}, nil, errors.New("manifesto de quarentena inválido")
	}
	manifestBytes, err := io.ReadAll(tr)
	if err != nil || int64(len(manifestBytes)) != h.Size {
		return trashManifest{}, nil, errors.New("manifesto de quarentena truncado")
	}
	var manifest trashManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil || manifest.Version != trashVersion {
		return trashManifest{}, nil, errors.New("versão de quarentena não suportada")
	}
	seen := map[string]bool{}
	for _, entry := range manifest.Entries {
		if err := validateTrashEntry(entry); err != nil {
			return trashManifest{}, nil, err
		}
		if seen[entry.ID] {
			return trashManifest{}, nil, errors.New("id de quarentena duplicado")
		}
		seen[entry.ID] = true
	}
	data := make(map[string][]byte, len(manifest.Entries))
	dataSeen := map[string]bool{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil || h.Typeflag != tar.TypeReg || !strings.HasPrefix(h.Name, "entries/") {
			return trashManifest{}, nil, errors.New("entrada de quarentena inválida")
		}
		id := strings.TrimPrefix(h.Name, "entries/")
		if !seen[id] || dataSeen[id] || h.Size < 0 {
			return trashManifest{}, nil, errors.New("entrada de quarentena desconhecida ou duplicada")
		}
		entry := TrashEntry{}
		for _, candidate := range manifest.Entries {
			if candidate.ID == id {
				entry = candidate
				break
			}
		}
		if h.Size != entry.Size {
			return trashManifest{}, nil, errors.New("tamanho de quarentena divergente")
		}
		var buf bytes.Buffer
		hash := sha256.New()
		if _, err := io.Copy(io.MultiWriter(&buf, hash), tr); err != nil {
			return trashManifest{}, nil, err
		}
		if hex.EncodeToString(hash.Sum(nil)) != entry.Hash {
			return trashManifest{}, nil, errors.New("hash de quarentena divergente")
		}
		data[id] = buf.Bytes()
		dataSeen[id] = true
	}
	if _, err := io.Copy(io.Discard, r); err != nil {
		return trashManifest{}, nil, fmt.Errorf("falha de integridade da quarentena: %w", err)
	}
	if len(data) != len(manifest.Entries) {
		return trashManifest{}, nil, errors.New("quarentena sem conteúdo de uma entrada")
	}
	sort.Slice(manifest.Entries, func(i, j int) bool { return manifest.Entries[i].ID < manifest.Entries[j].ID })
	return manifest, data, nil
}

func writeTrashArchive(path, password string, manifest trashManifest, data map[string][]byte) (err error) {
	if manifest.Version != trashVersion {
		return errors.New("versão de quarentena não suportada")
	}
	sort.Slice(manifest.Entries, func(i, j int) bool { return manifest.Entries[i].ID < manifest.Entries[j].ID })
	for _, entry := range manifest.Entries {
		if err := validateTrashEntry(entry); err != nil {
			return err
		}
		b, ok := data[entry.ID]
		if !ok || int64(len(b)) != entry.Size {
			return errors.New("conteúdo de quarentena ausente")
		}
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
	}()
	recipient, err := newRecipient(password)
	if err != nil {
		return err
	}
	w, err := age.Encrypt(f, recipient)
	if err != nil {
		return err
	}
	tw := tar.NewWriter(w)
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0600, Size: int64(len(manifestBytes)), Typeflag: tar.TypeReg}); err != nil {
		return err
	}
	if _, err := tw.Write(manifestBytes); err != nil {
		return err
	}
	for _, entry := range manifest.Entries {
		b := data[entry.ID]
		if err := tw.WriteHeader(&tar.Header{Name: "entries/" + entry.ID, Mode: 0600, Size: int64(len(b)), Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		if _, err := tw.Write(b); err != nil {
			return err
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

func (v *Vault) prepareTrash(journal reviewJournal, password string) error {
	if len(journal.Deletes) == 0 {
		return nil
	}
	if password == "" {
		return errors.New("a senha é necessária para criar a quarentena")
	}
	next := v.meta("txn", "trash-next.age")
	if exists(next) {
		if manifest, _, err := readTrashArchiveAll(next, password); err == nil && trashManifestContainsDeletes(manifest, journal.Deletes) {
			return nil
		}
		if err := os.Remove(next); err != nil {
			return err
		}
	}
	manifest := trashManifest{Version: trashVersion, Entries: []TrashEntry{}}
	data := map[string][]byte{}
	if exists(trashPath(v)) {
		var err error
		manifest, data, err = readTrashArchiveAll(trashPath(v), password)
		if err != nil {
			return err
		}
	}
	clock := v.now
	if clock == nil {
		clock = time.Now
	}
	now := clock()
	for _, file := range journal.Deletes {
		b, err := os.ReadFile(filepath.Join(v.notes(), filepath.FromSlash(file.Name)))
		if err != nil {
			return err
		}
		if int64(len(b)) != file.Size || digestBytes(b) != file.Hash {
			return fmt.Errorf("nota mudou; preservada: %s", file.Name)
		}
		entry := TrashEntry{ID: trashEntryID(file, now), Name: file.Name, Hash: file.Hash, Size: file.Size, DeletedAt: now.UTC()}
		if err := validateTrashEntry(entry); err != nil {
			return err
		}
		manifest.Entries = append(manifest.Entries, entry)
		data[entry.ID] = b
	}
	if err := writeTrashArchive(next, password, manifest, data); err != nil {
		return err
	}
	if _, _, err := readTrashArchiveAll(next, password); err != nil {
		return err
	}
	return nil
}

func trashManifestContainsDeletes(manifest trashManifest, deletes []reviewFile) bool {
	for _, file := range deletes {
		found := false
		for _, entry := range manifest.Entries {
			if entry.Name == file.Name && entry.Hash == file.Hash && entry.Size == file.Size {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func digestBytes(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// TrashList returns metadata only and never exposes note bytes.
func (v *Vault) TrashList(password string) ([]TrashEntry, error) {
	if password == "" {
		return nil, errors.New("a senha não pode ser vazia")
	}
	if !exists(trashPath(v)) {
		return []TrashEntry{}, nil
	}
	manifest, _, err := readTrashArchiveAll(trashPath(v), password)
	if err != nil {
		return nil, err
	}
	return manifest.Entries, nil
}

// TrashEmpty removes only the encrypted quarantine after authentication.
func (v *Vault) TrashEmpty(password string) error {
	if password == "" {
		return errors.New("a senha não pode ser vazia")
	}
	for _, name := range []string{"txn", "prepare", "cleanup", "trash-next.age", "trash-previous.age"} {
		if exists(v.meta(name)) {
			return errors.New("há uma operação incompleta; recupere-a antes de esvaziar a quarentena")
		}
	}
	if !exists(trashPath(v)) {
		return nil
	}
	if _, _, err := readTrashArchiveAll(trashPath(v), password); err != nil {
		return err
	}
	if err := os.Remove(trashPath(v)); err != nil {
		return err
	}
	return syncDir(v.meta())
}

// TrashRestore restores one entry into the open private folder, refusing
// collisions and retaining the encrypted entry until publication succeeds.
func (v *Vault) TrashRestore(id, password string) error {
	if password == "" {
		return errors.New("a senha não pode ser vazia")
	}
	if err := safePath(id); err != nil || len(id) != 64 {
		return errors.New("id de quarentena inválido")
	}
	if exists(v.meta("txn")) || exists(v.meta("prepare")) || exists(v.meta("cleanup")) {
		return errors.New("há uma operação incompleta; recupere-a antes de restaurar")
	}
	info, err := checkDirectory(v.notes())
	if err != nil {
		return errors.New("a vault precisa estar desbloqueada")
	}
	_ = info
	current := trashPath(v)
	next := v.meta("trash-next.age")
	previous := v.meta("trash-previous.age")
	var manifest trashManifest
	var data map[string][]byte
	var target TrashEntry
	var targetBytes []byte
	destination := ""

	// A previous archive marks a restore publication that was interrupted.
	// It is the authoritative source for the entry until the destination has
	// been written and verified.
	if exists(previous) {
		oldManifest, oldData, err := readTrashArchiveAll(previous, password)
		if err != nil {
			return err
		}
		manifest, data = oldManifest, oldData
		target, targetBytes, err = trashEntry(manifest, data, id)
		if err != nil {
			return err
		}
		if exists(current) {
			currentManifest, _, err := readTrashArchiveAll(current, password)
			if err != nil {
				return err
			}
			for _, entry := range currentManifest.Entries {
				if entry.ID == id {
					return errors.New("estado de restauração da quarentena é ambíguo; preserve a transação")
				}
			}
		} else if exists(next) {
			if _, _, err := readTrashArchiveAll(next, password); err != nil {
				return err
			}
			if err := move(next, current); err != nil {
				return err
			}
		} else {
			return errors.New("estado de restauração da quarentena está incompleto; preserve a transação")
		}
	} else {
		var err error
		manifest, data, err = readTrashArchiveAll(current, password)
		if err != nil {
			return err
		}
		target, targetBytes, err = trashEntry(manifest, data, id)
		if err != nil {
			return err
		}
		destination = filepath.Join(v.notes(), filepath.FromSlash(target.Name))
		if existing, ok, err := portablePathCollision(destination); err != nil {
			return err
		} else if ok {
			return fmt.Errorf("destino já existe: %s", existing)
		}
		if exists(next) {
			if err := os.Remove(next); err != nil {
				return err
			}
		}
		delete(data, id)
		for i, entry := range manifest.Entries {
			if entry.ID == id {
				manifest.Entries = append(manifest.Entries[:i], manifest.Entries[i+1:]...)
				break
			}
		}
		if err := writeTrashArchive(next, password, manifest, data); err != nil {
			return err
		}
		if _, _, err := readTrashArchiveAll(next, password); err != nil {
			return err
		}
		if err := move(current, previous); err != nil {
			return err
		}
		if err := v.checkpoint("trash-restore-previous-moved"); err != nil {
			return err
		}
		if err := move(next, current); err != nil {
			return err
		}
		if err := v.checkpoint("trash-restore-published"); err != nil {
			return err
		}
	}

	if destination == "" {
		destination = filepath.Join(v.notes(), filepath.FromSlash(target.Name))
	}
	if existing, ok, err := portablePathCollision(destination); err != nil {
		return err
	} else if ok {
		// A matching destination means a prior interrupted attempt completed the
		// write; it is safe to finalize the archive publication.
		hash, size, digestErr := digest(destination)
		if digestErr != nil || hash != target.Hash || size != target.Size {
			return fmt.Errorf("destino já existe: %s", existing)
		}
	} else {
		if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
			return err
		}
		file, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		if _, err := file.Write(targetBytes); err != nil {
			_ = file.Close()
			return err
		}
		if err := file.Sync(); err != nil {
			_ = file.Close()
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
	}
	if err := v.checkpoint("trash-restore-written"); err != nil {
		return err
	}
	if err := os.Remove(previous); err != nil && !os.IsNotExist(err) {
		return err
	}
	return syncDir(v.meta())
}

func trashEntry(manifest trashManifest, data map[string][]byte, id string) (TrashEntry, []byte, error) {
	for _, entry := range manifest.Entries {
		if entry.ID == id {
			b, ok := data[id]
			if !ok {
				return TrashEntry{}, nil, errors.New("conteúdo de quarentena ausente")
			}
			return entry, b, nil
		}
	}
	return TrashEntry{}, nil, errors.New("entrada de quarentena não encontrada")
}

func portablePathCollision(path string) (string, bool, error) {
	parent := filepath.Dir(path)
	if !exists(parent) {
		return "", false, nil
	}
	if _, err := checkDirectory(parent); err != nil {
		return "", false, err
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		return "", false, err
	}
	base := filepath.Base(path)
	for _, entry := range entries {
		if strings.EqualFold(entry.Name(), base) {
			return entry.Name(), true, nil
		}
	}
	return "", false, nil
}
