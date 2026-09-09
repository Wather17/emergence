package vault

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gofrs/flock"
)

const metadata = ".emergence"

type config struct {
	Version int    `json:"version"`
	Folder  string `json:"folder"`
}

type transaction struct {
	Operation string  `json:"operation"`
	Entries   []entry `json:"entries,omitempty"`
}

type Vault struct {
	Root   string
	Folder string
	guard  *flock.Flock
	hook   func(string) error // fault injection at durable transaction boundaries
}

func validFolder(folder string) error {
	if err := safePath(folder); err != nil {
		return err
	}
	if strings.Contains(folder, "/") || strings.HasPrefix(folder, ".") {
		return errors.New("a pasta deve ter um nome simples, sem começar com ponto")
	}
	return nil
}

func writeNew(path string, data []byte) (err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() {
		if e := f.Close(); err == nil {
			err = e
		}
	}()
	if _, err = f.Write(data); err != nil {
		return err
	}
	return f.Sync()
}

func writeJSON(path string, value any) error {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return writeNew(path, append(b, '\n'))
}

func move(from, to string) error {
	if exists(to) {
		return fmt.Errorf("destino já existe: %s", to)
	}
	if err := os.Rename(from, to); err != nil {
		return err
	}
	if err := syncDir(filepath.Dir(to)); err != nil {
		return err
	}
	return syncDir(filepath.Dir(from))
}

func Init(root, folder, password string) error {
	if err := validFolder(folder); err != nil {
		return err
	}
	if password == "" {
		return errors.New("a senha não pode ser vazia")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	if _, err := plain(root); err != nil {
		return err
	}
	// Do not nest a second private vault inside an existing one.
	for p := root; ; p = filepath.Dir(p) {
		if exists(filepath.Join(p, metadata)) {
			return fmt.Errorf("já existe uma configuração em %s", p)
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	if exists(filepath.Join(root, folder)) {
		return fmt.Errorf("a pasta %q já existe; escolha um nome novo", folder)
	}
	stage := filepath.Join(root, ".emergence-init")
	if err := os.Mkdir(stage, 0700); err != nil {
		return fmt.Errorf("não foi possível preparar a inicialização (verifique %s): %w", stage, err)
	}
	// This directory is ours and contains no user notes, only an empty archive.
	defer os.RemoveAll(stage)
	if err := writeJSON(filepath.Join(stage, "config.json"), config{1, folder}); err != nil {
		return err
	}
	archive := filepath.Join(stage, "sealed.age")
	if err := encrypt(archive, "", password, nil); err != nil {
		return err
	}
	if _, err := decrypt(archive, password, ""); err != nil {
		return err
	}
	if err := syncDir(stage); err != nil {
		return err
	}
	return move(stage, filepath.Join(root, metadata))
}

func Open(start string) (*Vault, error) {
	root, err := filepath.Abs(start)
	if err != nil {
		return nil, err
	}
	for !exists(filepath.Join(root, metadata)) {
		parent := filepath.Dir(root)
		if parent == root {
			return nil, errors.New("vault não inicializada; execute emergence init na raiz da vault")
		}
		root = parent
	}
	m := filepath.Join(root, metadata)
	if err := validateTree(m); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(filepath.Join(m, "config.json"))
	if err != nil {
		return nil, err
	}
	var c config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	if c.Version != 1 {
		return nil, errors.New("versão de armazenamento não suportada")
	}
	if err := validFolder(c.Folder); err != nil {
		return nil, err
	}
	guard := flock.New(filepath.Join(m, "operation.lock"), flock.SetPermissions(0600))
	ok, err := guard.TryLock()
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("outra operação está em andamento nesta vault")
	}
	return &Vault{Root: root, Folder: c.Folder, guard: guard}, nil
}

func (v *Vault) Close() error { return v.guard.Close() }
func (v *Vault) meta(parts ...string) string {
	return filepath.Join(append([]string{v.Root, metadata}, parts...)...)
}
func (v *Vault) notes() string { return filepath.Join(v.Root, v.Folder) }
func (v *Vault) checkpoint(name string) error {
	if v.hook != nil {
		return v.hook(name)
	}
	return nil
}

func (v *Vault) Status() (string, error) {
	if exists(v.meta("txn")) || exists(v.meta("cleanup")) || exists(v.meta("prepare")) {
		return "incompleta — pode haver conteúdo aberto; repita o último comando", nil
	}
	if _, err := plain(v.meta("sealed.age")); err != nil {
		return "", err
	}
	if exists(v.notes()) {
		i, err := plain(v.notes())
		if err != nil {
			return "", err
		}
		if !i.IsDir() {
			return "", errors.New("o caminho das notas não é uma pasta")
		}
		return "aberta", nil
	}
	return "trancada", nil
}

// Only internal staging directories are recursively removed. User notes are
// removed individually after matching the authenticated snapshot.
func (v *Vault) cleanInternal(name string) error {
	p := v.meta(name)
	if !exists(p) {
		return nil
	}
	if err := validateTree(p); err != nil {
		return err
	}
	if err := os.RemoveAll(p); err != nil {
		return err
	}
	return syncDir(v.meta())
}

func (v *Vault) begin(op string, entries []entry) (*transaction, error) {
	if err := v.cleanInternal("cleanup"); err != nil {
		return nil, err
	}
	if err := v.cleanInternal("prepare"); err != nil {
		return nil, err
	}
	if exists(v.meta("txn")) {
		b, err := os.ReadFile(v.meta("txn", "journal.json"))
		if err != nil {
			return nil, err
		}
		var tx transaction
		if err := json.Unmarshal(b, &tx); err != nil {
			return nil, err
		}
		if tx.Operation != op {
			return nil, fmt.Errorf("operação %s incompleta; repita emergence %s", tx.Operation, tx.Operation)
		}
		for _, e := range tx.Entries {
			if err := safePath(e.Name); err != nil {
				return nil, err
			}
		}
		return &tx, nil
	}
	tx := &transaction{Operation: op, Entries: entries}
	if err := os.Mkdir(v.meta("prepare"), 0700); err != nil {
		return nil, err
	}
	if err := writeJSON(v.meta("prepare", "journal.json"), tx); err != nil {
		return nil, err
	}
	if err := syncDir(v.meta("prepare")); err != nil {
		return nil, err
	}
	if err := move(v.meta("prepare"), v.meta("txn")); err != nil {
		return nil, err
	}
	return tx, v.checkpoint("begun")
}

func (v *Vault) finish() error {
	if err := move(v.meta("txn"), v.meta("cleanup")); err != nil {
		return err
	}
	if err := v.checkpoint("finished"); err != nil {
		return err
	}
	return v.cleanInternal("cleanup")
}

func (v *Vault) mark(name string) error {
	if err := writeNew(v.meta("txn", name), []byte("ok\n")); err != nil {
		return err
	}
	return syncDir(v.meta("txn"))
}

func (v *Vault) Unlock(password string) error {
	if password == "" {
		return errors.New("a senha não pode ser vazia")
	}
	if !exists(v.meta("txn")) && exists(v.notes()) {
		if err := v.cleanInternal("cleanup"); err != nil {
			return err
		}
		return errors.New("a pasta já está aberta ou existe um conflito; nenhum arquivo foi sobrescrito")
	}
	if _, err := v.begin("unlock", nil); err != nil {
		return err
	}
	stage := v.meta("txn", "notes")
	// A verified stage that disappeared while the destination appeared means
	// the publish rename succeeded before interruption. Verify before cleanup.
	if exists(v.notes()) {
		if !exists(v.meta("txn", "ready")) || exists(stage) {
			return errors.New("conflito com a pasta de notas; arquivos preservados")
		}
		want, err := decrypt(v.meta("sealed.age"), password, "")
		if err != nil {
			return err
		}
		got, err := snapshot(v.notes())
		if err != nil {
			return err
		}
		if !same(want, got) {
			return errors.New("a pasta publicada foi alterada; preserve suas notas antes de recuperar a operação")
		}
		return v.finish()
	}
	if exists(stage) {
		if err := validateTree(stage); err != nil {
			return err
		}
		if err := os.RemoveAll(stage); err != nil {
			return err
		}
	}
	if exists(v.meta("txn", "ready")) {
		if err := os.Remove(v.meta("txn", "ready")); err != nil {
			return err
		}
	}
	if err := os.Mkdir(stage, 0700); err != nil {
		return err
	}
	want, err := decrypt(v.meta("sealed.age"), password, stage)
	if err != nil {
		// Auth failure must leave no partially decrypted files behind.
		if cleanupErr := v.finish(); cleanupErr != nil {
			return fmt.Errorf("%w; limpeza incompleta: %v", err, cleanupErr)
		}
		return err
	}
	got, err := snapshot(stage)
	if err != nil {
		return err
	}
	if !same(want, got) {
		return errors.New("conteúdo extraído não corresponde ao arquivo")
	}
	// Flush directory entries as well as file contents on Linux.
	if err := filepath.WalkDir(stage, func(p string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.IsDir() {
			return syncDir(p)
		}
		return nil
	}); err != nil {
		return err
	}
	if err := v.mark("ready"); err != nil {
		return err
	}
	if err := v.checkpoint("unpacked"); err != nil {
		return err
	}
	if err := move(stage, v.notes()); err != nil {
		return err
	}
	if err := v.checkpoint("published"); err != nil {
		return err
	}
	return v.finish()
}

func (v *Vault) Lock(password string) error {
	if password == "" {
		return errors.New("a senha não pode ser vazia")
	}
	if err := v.cleanInternal("cleanup"); err != nil {
		return err
	}
	if !exists(v.meta("txn")) && !exists(v.notes()) {
		return errors.New("a pasta já está trancada")
	}
	var entries []entry
	if !exists(v.meta("txn")) {
		// Validate the password against the existing archive before any changes.
		if _, err := decrypt(v.meta("sealed.age"), password, ""); err != nil {
			return err
		}
		var err error
		entries, err = snapshot(v.notes())
		if err != nil {
			return err
		}
	}
	tx, err := v.begin("lock", entries)
	if err != nil {
		return err
	}
	candidate := v.meta("txn", "next.age")
	if !exists(v.meta("txn", "ready")) {
		if _, err := decrypt(v.meta("sealed.age"), password, ""); err != nil {
			return err
		}
		current, err := snapshot(v.notes())
		if err != nil {
			return err
		}
		if !same(current, tx.Entries) {
			// Before ready, no original has been removed. Discard this attempt;
			// the next lock will take a fresh snapshot including the edits.
			if err := v.finish(); err != nil {
				return err
			}
			return errors.New("notas mudaram; arquivos preservados. Encerre a edição e repita lock")
		}
		if exists(candidate) {
			if err := os.Remove(candidate); err != nil {
				return err
			}
		}
		if err := encrypt(candidate, v.notes(), password, tx.Entries); err != nil {
			return err
		}
		verified, err := decrypt(candidate, password, "")
		if err != nil {
			return err
		}
		if !same(verified, tx.Entries) {
			return errors.New("verificação do novo arquivo falhou")
		}
		if err := v.mark("ready"); err != nil {
			return err
		}
		if err := v.checkpoint("encrypted"); err != nil {
			return err
		}
	}
	// Both a fresh operation and a resumed one authenticate their snapshot.
	verifyPath := candidate
	if !exists(candidate) {
		verifyPath = v.meta("sealed.age")
	}
	verified, err := decrypt(verifyPath, password, "")
	if err != nil {
		return err
	}
	if !same(verified, tx.Entries) {
		return errors.New("arquivo criptografado diverge do registro; preserve .emergence para recuperação")
	}
	if exists(candidate) {
		if err := v.checkRemaining(tx.Entries, false); err != nil {
			return err
		}
		if !exists(v.meta("txn", "previous.age")) {
			if err := move(v.meta("sealed.age"), v.meta("txn", "previous.age")); err != nil {
				return err
			}
			if err := v.checkpoint("previous-moved"); err != nil {
				return err
			}
		}
		if err := move(candidate, v.meta("sealed.age")); err != nil {
			return err
		}
		if err := v.checkpoint("committed"); err != nil {
			return err
		}
	}
	if err := v.checkRemaining(tx.Entries, true); err != nil {
		return err
	}
	// Reverse order removes children before parents. Never recursively delete
	// the user's notes directory, including during recovery.
	for i := len(tx.Entries) - 1; i >= 0; i-- {
		e := tx.Entries[i]
		p := filepath.Join(v.notes(), filepath.FromSlash(e.Name))
		if !exists(p) {
			continue
		}
		if !e.Dir {
			h, n, err := digest(p)
			if err != nil {
				return err
			}
			if h != e.Hash || n != e.Size {
				return fmt.Errorf("arquivo mudou; preservado: %s", e.Name)
			}
		} else {
			if _, err := plain(p); err != nil {
				return err
			}
		}
		if err := os.Remove(p); err != nil {
			return fmt.Errorf("remoção incompleta de %s; feche o editor e repita lock: %w", e.Name, err)
		}
		if err := v.checkpoint("removed-entry"); err != nil {
			return err
		}
	}
	if exists(v.notes()) {
		if err := os.Remove(v.notes()); err != nil {
			return fmt.Errorf("restam arquivos abertos; preserve-os e repita lock: %w", err)
		}
	}
	if err := syncDir(v.Root); err != nil {
		return err
	}
	if err := v.checkpoint("removed"); err != nil {
		return err
	}
	return v.finish()
}

func (v *Vault) checkRemaining(want []entry, allowMissing bool) error {
	if !exists(v.notes()) {
		if allowMissing {
			return nil
		}
		return errors.New("a pasta aberta desapareceu")
	}
	got, err := snapshot(v.notes())
	if err != nil {
		return err
	}
	if !allowMissing && !same(got, want) {
		return errors.New("notas mudaram; nada foi removido. Preserve as alterações antes de recuperar a operação")
	}
	expected := map[string]entry{}
	for _, e := range want {
		expected[e.Name] = e
	}
	for _, e := range got {
		if expected[e.Name] != e {
			return fmt.Errorf("conteúdo novo ou alterado em %s; preservado. Consulte a recuperação no README", e.Name)
		}
	}
	return nil
}
