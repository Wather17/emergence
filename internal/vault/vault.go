package vault

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/gofrs/flock"
)

const metadata = ".emergence"

type config struct {
	Version int    `json:"version"`
	Folder  string `json:"folder"`
	Inbox   string `json:"inbox,omitempty"`
}

type transaction struct {
	Operation string  `json:"operation"`
	Entries   []entry `json:"entries,omitempty"`
}

type destroyRecord struct {
	Version  int    `json:"version"`
	Root     string `json:"root"`
	Notes    string `json:"notes"`
	Metadata string `json:"metadata"`
}

type reviewFile struct {
	Name string `json:"name"`
	Hash string `json:"sha256"`
	Size int64  `json:"size"`
}

type reviewMove struct {
	Source      reviewFile `json:"source"`
	Destination string     `json:"destination"`
}

type reviewJournal struct {
	Operation string       `json:"operation"`
	Inbox     string       `json:"inbox"`
	Deletes   []reviewFile `json:"deletes,omitempty"`
	Moves     []reviewMove `json:"moves,omitempty"`
}

type Vault struct {
	Root   string
	Folder string
	Inbox  string
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

func validInbox(inbox string) error {
	if inbox == "" {
		return nil
	}
	if err := safePath(inbox); err != nil {
		return err
	}
	return nil
}

// FindInboxCandidates returns relative directory paths whose names contain
// "inbox", case-insensitively. Internal metadata, the private folder and
// links are never considered candidates.
func FindInboxCandidates(root, privateFolder string) ([]string, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if _, err := plain(root); err != nil {
		return nil, err
	}
	candidates := []string{}
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := plain(path)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == metadata || rel == privateFolder || strings.HasPrefix(rel, metadata+"/") || strings.HasPrefix(rel, privateFolder+"/") {
			return filepath.SkipDir
		}
		if strings.Contains(strings.ToLower(filepath.Base(rel)), "inbox") {
			candidates = append(candidates, rel)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.Sort(candidates)
	return candidates, nil
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
	inbox := ""
	candidates, err := FindInboxCandidates(root, folder)
	if err != nil {
		return err
	}
	if len(candidates) == 1 {
		inbox = candidates[0]
	}
	if err := writeJSON(filepath.Join(stage, "config.json"), config{Version: 1, Folder: folder, Inbox: inbox}); err != nil {
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
	if err := validInbox(c.Inbox); err != nil {
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
	return &Vault{Root: root, Folder: c.Folder, Inbox: c.Inbox, guard: guard}, nil
}

func (v *Vault) Close() error {
	if v.guard == nil {
		return nil
	}
	err := v.guard.Close()
	v.guard = nil
	return err
}
func (v *Vault) meta(parts ...string) string {
	return filepath.Join(append([]string{v.Root, metadata}, parts...)...)
}
func (v *Vault) notes() string { return filepath.Join(v.Root, v.Folder) }
func (v *Vault) inbox() string {
	if v.Inbox == "" {
		return ""
	}
	return filepath.Join(v.Root, filepath.FromSlash(v.Inbox))
}

// InboxPath returns the configured Inbox relative to the vault root, or an
// empty string when init found none or the user has not selected one.
func (v *Vault) InboxPath() string { return v.Inbox }

func (v *Vault) InboxCandidates() ([]string, error) {
	return FindInboxCandidates(v.Root, v.Folder)
}

func (v *Vault) saveConfig() error {
	tmp := v.meta("config.next")
	if exists(tmp) {
		if err := validateTree(tmp); err != nil {
			return err
		}
		if err := os.RemoveAll(tmp); err != nil {
			return err
		}
	}
	if err := writeJSON(tmp, config{Version: 1, Folder: v.Folder, Inbox: v.Inbox}); err != nil {
		return err
	}
	if err := syncDir(v.meta()); err != nil {
		return err
	}
	if err := os.Remove(v.meta("config.json")); err != nil {
		return err
	}
	if err := os.Rename(tmp, v.meta("config.json")); err != nil {
		return err
	}
	return syncDir(v.meta())
}

func (v *Vault) SetInbox(candidate string) error {
	if err := validInbox(candidate); err != nil {
		return err
	}
	candidates, err := v.InboxCandidates()
	if err != nil {
		return err
	}
	if !slices.Contains(candidates, candidate) {
		return errors.New("Inbox inválida ou inexistente")
	}
	path := filepath.Join(v.Root, filepath.FromSlash(candidate))
	info, err := plain(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("Inbox não é uma pasta")
	}
	previous := v.Inbox
	v.Inbox = candidate
	if err := v.saveConfig(); err != nil {
		v.Inbox = previous
		return err
	}
	return nil
}

func (v *Vault) ReviewInProgress() bool { return exists(v.meta("txn", "review.json")) }

func (v *Vault) MarkdownNotes() ([]string, error) {
	if v.Inbox == "" {
		return nil, errors.New("Inbox não configurada; execute emergence inbox")
	}
	info, err := plain(v.inbox())
	if err != nil {
		return nil, errors.New("Inbox ausente ou renomeada; execute emergence inbox")
	}
	if !info.IsDir() {
		return nil, errors.New("Inbox não é uma pasta; execute emergence inbox")
	}
	notesInfo, err := plain(v.notes())
	if err != nil {
		return nil, errors.New("a vault precisa estar desbloqueada")
	}
	if !notesInfo.IsDir() {
		return nil, errors.New("a vault precisa estar desbloqueada")
	}
	paths := []string{}
	err = filepath.WalkDir(v.notes(), func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == v.notes() {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return filepath.SkipDir
		}
		info, err := plain(path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || !strings.EqualFold(filepath.Ext(d.Name()), ".md") {
			return nil
		}
		rel, err := filepath.Rel(v.notes(), path)
		if err != nil {
			return err
		}
		paths = append(paths, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.Sort(paths)
	return paths, nil
}

func (v *Vault) reviewJournalPath() string { return v.meta("txn", "review.json") }

func (v *Vault) readReviewJournal() (reviewJournal, error) {
	b, err := os.ReadFile(v.reviewJournalPath())
	if err != nil {
		return reviewJournal{}, err
	}
	var journal reviewJournal
	if err := json.Unmarshal(b, &journal); err != nil {
		return reviewJournal{}, err
	}
	if journal.Operation != "review" || journal.Inbox == "" {
		return reviewJournal{}, errors.New("diário de revisão inválido")
	}
	if err := validInbox(journal.Inbox); err != nil {
		return reviewJournal{}, err
	}
	for _, f := range journal.Deletes {
		if err := safePath(f.Name); err != nil {
			return reviewJournal{}, err
		}
	}
	for _, m := range journal.Moves {
		if err := safePath(m.Source.Name); err != nil {
			return reviewJournal{}, err
		}
		if err := safePath(m.Destination); err != nil {
			return reviewJournal{}, err
		}
		if filepath.ToSlash(filepath.Dir(filepath.FromSlash(m.Destination))) != journal.Inbox {
			return reviewJournal{}, errors.New("diário de revisão aponta para uma Inbox diferente")
		}
	}
	return journal, nil
}

func (v *Vault) beginReview(deleteNames []string) (reviewJournal, error) {
	if err := v.cleanInternal("cleanup"); err != nil {
		return reviewJournal{}, err
	}
	if err := v.cleanInternal("prepare"); err != nil {
		return reviewJournal{}, err
	}
	if exists(v.meta("txn")) {
		if exists(v.reviewJournalPath()) {
			return v.readReviewJournal()
		}
		return reviewJournal{}, errors.New("outra operação está incompleta; repita o mesmo comando")
	}
	if v.Inbox == "" {
		return reviewJournal{}, errors.New("Inbox não configurada; execute emergence inbox")
	}
	inboxInfo, err := plain(v.inbox())
	if err != nil || !inboxInfo.IsDir() {
		return reviewJournal{}, errors.New("Inbox ausente ou renomeada; execute emergence inbox")
	}
	if rel, err := filepath.Rel(v.notes(), v.inbox()); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return reviewJournal{}, errors.New("Inbox não pode ficar dentro da pasta privada")
	}
	names, err := v.MarkdownNotes()
	if err != nil {
		return reviewJournal{}, err
	}
	all := map[string]reviewFile{}
	for _, name := range names {
		hash, size, err := digest(filepath.Join(v.notes(), filepath.FromSlash(name)))
		if err != nil {
			return reviewJournal{}, err
		}
		all[name] = reviewFile{Name: name, Hash: hash, Size: size}
	}
	selected := map[string]bool{}
	for _, name := range deleteNames {
		if err := safePath(name); err != nil {
			return reviewJournal{}, err
		}
		if selected[name] {
			return reviewJournal{}, fmt.Errorf("nota selecionada mais de uma vez: %s", name)
		}
		if _, ok := all[name]; !ok {
			return reviewJournal{}, fmt.Errorf("nota selecionada não encontrada: %s", name)
		}
		selected[name] = true
	}
	journal := reviewJournal{Operation: "review", Inbox: v.Inbox}
	seenDestinations := map[string]string{}
	existingDestinations := map[string]string{}
	inboxEntries, err := os.ReadDir(v.inbox())
	if err != nil {
		return reviewJournal{}, err
	}
	for _, inboxEntry := range inboxEntries {
		path := filepath.Join(v.inbox(), inboxEntry.Name())
		if _, err := plain(path); err != nil {
			return reviewJournal{}, err
		}
		existingDestinations[strings.ToLower(inboxEntry.Name())] = inboxEntry.Name()
	}
	for _, name := range names {
		file := all[name]
		if selected[name] {
			journal.Deletes = append(journal.Deletes, file)
			continue
		}
		base := filepath.Base(filepath.FromSlash(name))
		destination := filepath.ToSlash(filepath.Join(filepath.FromSlash(v.Inbox), base))
		if previous, ok := seenDestinations[strings.ToLower(base)]; ok {
			return reviewJournal{}, fmt.Errorf("colisão de nomes entre %s e %s", previous, name)
		}
		seenDestinations[strings.ToLower(base)] = name
		if existing, ok := existingDestinations[strings.ToLower(base)]; ok {
			return reviewJournal{}, fmt.Errorf("colisão na Inbox: %s", filepath.Join(v.inbox(), existing))
		}
		journal.Moves = append(journal.Moves, reviewMove{Source: file, Destination: destination})
	}
	if err := os.Mkdir(v.meta("txn"), 0700); err != nil {
		return reviewJournal{}, err
	}
	if err := writeJSON(v.reviewJournalPath(), journal); err != nil {
		return reviewJournal{}, err
	}
	if err := syncDir(v.meta("txn")); err != nil {
		return reviewJournal{}, err
	}
	return journal, v.checkpoint("review-begun")
}

func (v *Vault) applyReview(journal reviewJournal) error {
	if journal.Inbox != v.Inbox {
		return errors.New("a Inbox configurada mudou; preserve a transação e execute a revisão novamente")
	}
	for _, file := range journal.Deletes {
		path := filepath.Join(v.notes(), filepath.FromSlash(file.Name))
		if !exists(path) {
			continue
		}
		hash, size, err := digest(path)
		if err != nil {
			return err
		}
		if hash != file.Hash || size != file.Size {
			return fmt.Errorf("nota mudou; preservada: %s", file.Name)
		}
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("não foi possível apagar %s: %w", file.Name, err)
		}
		if err := v.checkpoint("review-deleted"); err != nil {
			return err
		}
	}
	for _, moveEntry := range journal.Moves {
		source := filepath.Join(v.notes(), filepath.FromSlash(moveEntry.Source.Name))
		destination := filepath.Join(v.Root, filepath.FromSlash(moveEntry.Destination))
		if filepath.ToSlash(filepath.Dir(filepath.FromSlash(moveEntry.Destination))) != journal.Inbox {
			return errors.New("destino de revisão fora da Inbox configurada")
		}
		if !exists(source) {
			if !exists(destination) {
				return fmt.Errorf("nota de origem desapareceu: %s", moveEntry.Source.Name)
			}
			hash, size, err := digest(destination)
			if err != nil || hash != moveEntry.Source.Hash || size != moveEntry.Source.Size {
				return fmt.Errorf("destino divergente na recuperação: %s", destination)
			}
			continue
		}
		hash, size, err := digest(source)
		if err != nil {
			return err
		}
		if hash != moveEntry.Source.Hash || size != moveEntry.Source.Size {
			return fmt.Errorf("nota mudou; preservada: %s", moveEntry.Source.Name)
		}
		if exists(destination) {
			return fmt.Errorf("colisão na Inbox: %s", destination)
		}
		if err := os.Rename(source, destination); err != nil {
			return fmt.Errorf("não foi possível mover %s: %w", moveEntry.Source.Name, err)
		}
		if err := syncDir(filepath.Dir(destination)); err != nil {
			return err
		}
		if err := v.checkpoint("review-moved"); err != nil {
			return err
		}
	}
	if err := move(v.meta("txn"), v.meta("cleanup")); err != nil {
		return err
	}
	return v.cleanInternal("cleanup")
}

// Review applies the selected deletions and moves every remaining Markdown
// note to the configured Inbox. An existing review journal is resumed.
func (v *Vault) Review(deleteNames []string) error {
	var journal reviewJournal
	var err error
	if v.ReviewInProgress() {
		journal, err = v.readReviewJournal()
	} else {
		journal, err = v.beginReview(deleteNames)
	}
	if err != nil {
		return err
	}
	return v.applyReview(journal)
}
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

// DestroyTargets returns the closed set of paths that destroy may remove. It
// performs all structural validation before the destructive confirmation.
func (v *Vault) DestroyTargets() ([]string, error) {
	rootInfo, err := plain(v.Root)
	if err != nil {
		return nil, err
	}
	if !rootInfo.IsDir() {
		return nil, errors.New("a raiz da vault não é uma pasta")
	}
	metadataInfo, err := plain(v.meta())
	if err != nil {
		return nil, fmt.Errorf("metadata inválida: %w", err)
	}
	if !metadataInfo.IsDir() {
		return nil, errors.New("metadata inválida: .emergence não é uma pasta")
	}
	if exists(v.meta("txn")) || exists(v.meta("cleanup")) || exists(v.meta("prepare")) {
		return nil, errors.New("há uma operação incompleta; recupere-a antes de destruir a vault")
	}
	sealedInfo, err := plain(v.meta("sealed.age"))
	if err != nil {
		return nil, fmt.Errorf("arquivo criptografado inválido: %w", err)
	}
	if !sealedInfo.Mode().IsRegular() {
		return nil, errors.New("arquivo criptografado inválido: sealed.age não é um arquivo regular")
	}
	if err := validateTree(v.meta()); err != nil {
		return nil, fmt.Errorf("metadata inválida: %w", err)
	}
	targets := []string{}
	if exists(v.notes()) {
		notesInfo, err := plain(v.notes())
		if err != nil {
			return nil, fmt.Errorf("pasta privada inválida: %w", err)
		}
		if !notesInfo.IsDir() {
			return nil, errors.New("pasta privada inválida: o caminho não é uma pasta")
		}
		if err := validateTree(v.notes()); err != nil {
			return nil, fmt.Errorf("pasta privada inválida: %w", err)
		}
		targets = append(targets, v.notes())
	}
	targets = append(targets, v.meta())
	return targets, nil
}

// ValidateDestroyCwd prevents a process from removing the directory it is
// currently using, while still allowing invocation from the vault root or any
// ordinary sibling subfolder.
func (v *Vault) ValidateDestroyCwd(start string) error {
	abs, err := filepath.Abs(start)
	if err != nil {
		return err
	}
	for _, target := range []string{v.notes(), v.meta()} {
		rel, err := filepath.Rel(target, abs)
		if err != nil {
			return err
		}
		if rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
			return errors.New("execute destroy a partir da raiz ou de uma subpasta fora dos alvos")
		}
	}
	return nil
}

// ValidatePassword authenticates the current sealed archive without writing
// or removing anything.
func (v *Vault) ValidatePassword(password string) error {
	if password == "" {
		return errors.New("a senha não pode ser vazia")
	}
	if _, err := decrypt(v.meta("sealed.age"), password, ""); err != nil {
		return err
	}
	return nil
}

func (v *Vault) destroyMarker() string { return filepath.Join(v.Root, ".emergence-destroy") }

func (v *Vault) readDestroyRecord() (destroyRecord, error) {
	b, err := os.ReadFile(v.destroyMarker())
	if err != nil {
		return destroyRecord{}, err
	}
	var record destroyRecord
	if err := json.Unmarshal(b, &record); err != nil {
		return destroyRecord{}, fmt.Errorf("marcador de destruição inválido: %w", err)
	}
	if record.Version != 1 || filepath.Clean(record.Root) != filepath.Clean(v.Root) ||
		filepath.Clean(record.Metadata) != filepath.Clean(v.meta()) || filepath.Clean(record.Notes) != filepath.Clean(v.notes()) {
		return destroyRecord{}, errors.New("marcador de destruição não corresponde a esta vault")
	}
	return record, nil
}

func (v *Vault) removeMetadata() error {
	if err := validateTree(v.meta()); err != nil {
		return err
	}
	entries, err := os.ReadDir(v.meta())
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Name() == "operation.lock" {
			continue
		}
		p := filepath.Join(v.meta(), e.Name())
		if err := os.RemoveAll(p); err != nil {
			return err
		}
	}
	if err := syncDir(v.meta()); err != nil {
		return err
	}
	if err := v.Close(); err != nil {
		return err
	}
	if err := os.Remove(v.meta("operation.lock")); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Remove(v.meta()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return syncDir(v.Root)
}

// Destroy irreversibly removes the private folder and Emergence metadata. A
// marker outside the targets makes an interrupted removal explicit and allows
// a later invocation on the still-open Vault object to continue safely.
func (v *Vault) Destroy(password string) error {
	if password == "" {
		return errors.New("a senha não pode ser vazia")
	}
	marker := v.destroyMarker()
	resuming := exists(marker)
	var record destroyRecord
	if resuming {
		var err error
		record, err = v.readDestroyRecord()
		if err != nil {
			return err
		}
		if exists(v.meta("sealed.age")) {
			if err := v.ValidatePassword(password); err != nil {
				return err
			}
		}
		for _, p := range []string{record.Notes, record.Metadata} {
			if exists(p) {
				if err := validateTree(p); err != nil {
					return err
				}
			}
		}
	} else {
		targets, err := v.DestroyTargets()
		if err != nil {
			return err
		}
		if err := v.ValidatePassword(password); err != nil {
			return err
		}
		record = destroyRecord{Version: 1, Root: v.Root, Notes: v.notes(), Metadata: v.meta()}
		if len(targets) == 0 {
			return errors.New("nenhum alvo de destruição foi encontrado")
		}
		if err := writeJSON(marker, record); err != nil {
			return err
		}
		if err := syncDir(v.Root); err != nil {
			return err
		}
	}
	if err := v.checkpoint("destroy-prepared"); err != nil {
		return err
	}
	if exists(record.Notes) {
		if err := os.RemoveAll(record.Notes); err != nil {
			return fmt.Errorf("destruição incompleta em %s: %w", record.Notes, err)
		}
		if err := syncDir(v.Root); err != nil {
			return err
		}
	}
	if err := v.checkpoint("destroy-notes"); err != nil {
		return err
	}
	if exists(record.Metadata) {
		if err := v.removeMetadata(); err != nil {
			return fmt.Errorf("destruição incompleta em %s: %w", record.Metadata, err)
		}
	}
	if err := v.checkpoint("destroy-metadata"); err != nil {
		return err
	}
	if err := os.Remove(marker); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("destruição concluída, mas o marcador permaneceu: %w", err)
	}
	return syncDir(v.Root)
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
