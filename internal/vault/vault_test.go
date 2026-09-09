package vault

import (
	"archive/tar"
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
)

const testPassword = "a long test passphrase"

func TestMain(m *testing.M) {
	newRecipient = func(p string) (*age.ScryptRecipient, error) {
		r, err := age.NewScryptRecipient(p)
		if err == nil {
			r.SetWorkFactor(10)
		}
		return r, err
	}
	os.Exit(m.Run())
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func put(t *testing.T, path string, data []byte) {
	t.Helper()
	must(t, os.MkdirAll(filepath.Dir(path), 0700))
	must(t, os.WriteFile(path, data, 0600))
}
func read(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	must(t, err)
	return b
}

func fixture(t *testing.T) *Vault {
	t.Helper()
	root := t.TempDir()
	must(t, Init(root, "Morning Pages", testPassword))
	v, err := Open(root)
	must(t, err)
	t.Cleanup(func() { _ = v.Close() })
	return v
}

func populate(t *testing.T, v *Vault) []entry {
	t.Helper()
	must(t, v.Unlock(testPassword))
	put(t, filepath.Join(v.notes(), "2026-09-09.md"), []byte("# Manhã\nUma ideia privada.\n"))
	put(t, filepath.Join(v.notes(), "ideias com acentos", "café.md"), []byte("café ☕\n"))
	put(t, filepath.Join(v.notes(), "anexo.bin"), bytes.Repeat([]byte{0, 1, 255, 42}, 40000))
	must(t, os.Mkdir(filepath.Join(v.notes(), "vazia"), 0700))
	got, err := snapshot(v.notes())
	must(t, err)
	return got
}

func TestRoundTrip(t *testing.T) {
	v := fixture(t)
	status, err := v.Status()
	must(t, err)
	if status != "trancada" {
		t.Fatal(status)
	}
	must(t, v.Unlock(testPassword))
	must(t, v.Lock(testPassword)) // empty folder
	want := populate(t, v)
	must(t, v.Lock(testPassword))
	if exists(v.notes()) || exists(v.meta("txn")) {
		t.Fatal("plaintext or transaction remains")
	}
	b := read(t, v.meta("sealed.age"))
	if bytes.Contains(b, []byte("café")) || bytes.Contains(b, []byte("2026-09-09")) {
		t.Fatal("plaintext leaked")
	}
	must(t, v.Unlock(testPassword))
	got, err := snapshot(v.notes())
	must(t, err)
	if !same(got, want) {
		t.Fatal("round trip changed files")
	}
	status, err = v.Status()
	must(t, err)
	if status != "aberta" {
		t.Fatal(status)
	}
}

func TestWrongPasswordAndCorruption(t *testing.T) {
	v := fixture(t)
	want := populate(t, v)
	before := read(t, v.meta("sealed.age"))
	if err := v.Lock("wrong"); err == nil {
		t.Fatal("wrong password accepted")
	}
	got, err := snapshot(v.notes())
	must(t, err)
	if !same(got, want) || !bytes.Equal(before, read(t, v.meta("sealed.age"))) {
		t.Fatal("wrong password changed data")
	}
	must(t, v.Lock(testPassword))
	sealed := read(t, v.meta("sealed.age"))
	for _, kind := range []string{"wrong", "truncated", "tampered"} {
		t.Run(kind, func(t *testing.T) {
			data := bytes.Clone(sealed)
			p := testPassword
			switch kind {
			case "wrong":
				p = "wrong"
			case "truncated":
				data = data[:len(data)-1]
			case "tampered":
				data[len(data)-1] ^= 1
			}
			put(t, v.meta("sealed.age"), data)
			if err := v.Unlock(p); err == nil {
				t.Fatal("invalid archive accepted")
			}
			if exists(v.notes()) || exists(v.meta("txn")) || exists(v.meta("cleanup")) {
				t.Fatal("partial plaintext left behind")
			}
		})
	}
	put(t, v.meta("sealed.age"), sealed)
	must(t, v.Unlock(testPassword))
}

func TestLockRecovery(t *testing.T) {
	for _, point := range []string{"begun", "encrypted", "previous-moved", "committed", "removed-entry", "removed", "finished"} {
		t.Run(point, func(t *testing.T) {
			v := fixture(t)
			want := populate(t, v)
			stop := errors.New("simulated interruption")
			v.hook = func(p string) error {
				if p == point {
					return stop
				}
				return nil
			}
			if err := v.Lock(testPassword); !errors.Is(err, stop) {
				t.Fatalf("expected interruption: %v", err)
			}
			status, err := v.Status()
			must(t, err)
			if !strings.HasPrefix(status, "incompleta") {
				t.Fatal(status)
			}
			// Drop and reacquire the OS lock like a new process.
			must(t, v.Close())
			reopened, err := Open(v.Root)
			must(t, err)
			*v = *reopened
			err = v.Lock(testPassword)
			if point == "finished" {
				if err == nil || !strings.Contains(err.Error(), "já está trancada") {
					t.Fatal(err)
				}
			} else {
				must(t, err)
			}
			if exists(v.notes()) {
				t.Fatal("notes remain")
			}
			must(t, v.Unlock(testPassword))
			got, err := snapshot(v.notes())
			must(t, err)
			if !same(got, want) {
				t.Fatal("recovery lost data")
			}
		})
	}
}

func TestUnlockRecovery(t *testing.T) {
	for _, point := range []string{"begun", "unpacked", "published", "finished"} {
		t.Run(point, func(t *testing.T) {
			v := fixture(t)
			want := populate(t, v)
			must(t, v.Lock(testPassword))
			stop := errors.New("simulated interruption")
			v.hook = func(p string) error {
				if p == point {
					return stop
				}
				return nil
			}
			if err := v.Unlock(testPassword); !errors.Is(err, stop) {
				t.Fatal(err)
			}
			must(t, v.Close())
			reopened, err := Open(v.Root)
			must(t, err)
			*v = *reopened
			err = v.Unlock(testPassword)
			if point == "finished" {
				if err == nil {
					t.Fatal("expected already open")
				}
			} else {
				must(t, err)
			}
			got, err := snapshot(v.notes())
			must(t, err)
			if !same(got, want) {
				t.Fatal("recovery lost data")
			}
		})
	}
}

func TestConcurrentEditsPreserved(t *testing.T) {
	for _, point := range []string{"begun", "encrypted", "committed", "removed-entry"} {
		t.Run(point, func(t *testing.T) {
			v := fixture(t)
			populate(t, v)
			path := filepath.Join(v.notes(), "2026-09-09.md")
			change := []byte("new thoughts")
			fired := false
			v.hook = func(p string) error {
				if p == point && !fired {
					fired = true
					put(t, path, change)
				}
				return nil
			}
			if err := v.Lock(testPassword); err == nil {
				t.Fatal("changed data silently removed")
			}
			if !bytes.Equal(read(t, path), change) {
				t.Fatal("edit lost")
			}
		})
	}
}

func TestConflictsAndDiscovery(t *testing.T) {
	v := fixture(t)
	if _, err := Open(v.Root); err == nil {
		t.Fatal("concurrent operation accepted")
	}
	if err := Init(v.Root, "Other", testPassword); err == nil {
		t.Fatal("reinitialization accepted")
	}
	must(t, os.Mkdir(v.notes(), 0700))
	put(t, filepath.Join(v.notes(), "outside.md"), []byte("untouched"))
	if err := v.Unlock(testPassword); err == nil {
		t.Fatal("folder conflict accepted")
	}
	if string(read(t, filepath.Join(v.notes(), "outside.md"))) != "untouched" {
		t.Fatal("conflict overwritten")
	}
	sub := filepath.Join(v.Root, "another", "subfolder")
	must(t, os.MkdirAll(sub, 0700))
	must(t, v.Close())
	reopened, err := Open(sub)
	must(t, err)
	*v = *reopened
	if v.Root != filepath.Dir(filepath.Dir(sub)) {
		t.Fatal("discovery failed")
	}
}

func TestDiscoveryIgnoresNestedEmergenceDirectories(t *testing.T) {
	v := fixture(t)
	must(t, v.Unlock(testPassword))
	nested := filepath.Join(v.notes(), "project", ".emergence")
	must(t, os.MkdirAll(nested, 0700))
	subfolder := filepath.Join(nested, "subfolder")
	must(t, os.Mkdir(subfolder, 0700))
	must(t, v.Close())
	reopened, err := Open(subfolder)
	must(t, err)
	defer reopened.Close()
	if reopened.Root != v.Root {
		t.Fatalf("nested .emergence shadowed vault: got %s, want %s", reopened.Root, v.Root)
	}
}

func TestSafePathReservesEmergenceComponent(t *testing.T) {
	for _, name := range []string{".emergence", "notes/.emergence/file.md", "NOTES/.EMERGENCE"} {
		if err := safePath(name); err == nil {
			t.Fatalf("reserved path accepted: %s", name)
		}
	}
}

func TestStatusRejectsNonRegularSealedArchive(t *testing.T) {
	v := fixture(t)
	sealed := v.meta("sealed.age")
	backup := sealed + ".backup"
	must(t, os.Rename(sealed, backup))
	must(t, os.Mkdir(sealed, 0700))
	if _, err := v.Status(); err == nil {
		t.Fatal("status accepted a directory as sealed.age")
	}
	must(t, os.Remove(sealed))
	must(t, os.Rename(backup, sealed))
	if status, err := v.Status(); err != nil || status != "trancada" {
		t.Fatalf("valid sealed archive rejected: %s %v", status, err)
	}
}

func TestStatusRejectsMissingSealedArchive(t *testing.T) {
	v := fixture(t)
	must(t, os.Remove(v.meta("sealed.age")))
	if _, err := v.Status(); err == nil {
		t.Fatal("status accepted missing sealed.age")
	}
}

func TestDoctorHealthyAndReadOnly(t *testing.T) {
	root := t.TempDir()
	must(t, os.Mkdir(filepath.Join(root, "Inbox"), 0700))
	must(t, Init(root, "Morning Pages", testPassword))
	before := read(t, filepath.Join(root, metadata, "sealed.age"))
	report, err := Doctor(filepath.Join(root, "Inbox"), false, "")
	must(t, err)
	if report.Root != root || report.HasErrors() {
		t.Fatalf("unexpected healthy report: %#v", report)
	}
	if !bytes.Equal(before, read(t, filepath.Join(root, metadata, "sealed.age"))) {
		t.Fatal("doctor changed sealed archive")
	}
	for _, check := range report.Checks {
		if strings.Contains(check.Message, "senha") || strings.Contains(check.Message, "Markdown") {
			t.Fatalf("unexpected sensitive diagnostic: %#v", check)
		}
	}
}

func TestDoctorAggregatesStructuralProblems(t *testing.T) {
	root := t.TempDir()
	must(t, Init(root, "Morning Pages", testPassword))
	meta := filepath.Join(root, metadata)
	must(t, os.WriteFile(filepath.Join(meta, "config.json"), []byte("{"), 0600))
	must(t, os.Mkdir(filepath.Join(meta, "txn"), 0700))
	must(t, os.WriteFile(filepath.Join(root, ".emergence-destroy"), []byte("{}"), 0600))
	report, err := Doctor(root, false, "")
	must(t, err)
	if !report.HasErrors() {
		t.Fatalf("invalid vault was reported healthy: %#v", report)
	}
	seen := map[string]bool{}
	for _, check := range report.Checks {
		seen[check.Name] = true
	}
	for _, name := range []string{"config", "txn", "destroy-marker"} {
		if !seen[name] {
			t.Fatalf("missing %s diagnostic: %#v", name, report.Checks)
		}
	}
}

func TestDoctorCheckArchiveDoesNotCreatePlaintext(t *testing.T) {
	v := fixture(t)
	before := read(t, v.meta("sealed.age"))
	valid, err := Doctor(v.Root, true, testPassword)
	must(t, err)
	if valid.HasErrors() {
		t.Fatalf("valid archive was reported invalid: %#v", valid)
	}
	report, err := Doctor(v.Root, true, "wrong")
	must(t, err)
	if !report.HasErrors() {
		t.Fatal("wrong archive password was reported healthy")
	}
	if exists(v.notes()) || !bytes.Equal(before, read(t, v.meta("sealed.age"))) {
		t.Fatal("archive check changed vault state")
	}
}

func TestRotatePasswordPreservesArchive(t *testing.T) {
	v := fixture(t)
	want := populate(t, v)
	must(t, v.Lock(testPassword))
	before := read(t, v.meta("sealed.age"))
	must(t, v.RotatePassword(testPassword, "a new passphrase"))
	if bytes.Equal(before, read(t, v.meta("sealed.age"))) {
		t.Fatal("rotation did not publish a new archive")
	}
	if err := v.ValidatePassword(testPassword); err == nil {
		t.Fatal("old password still authenticates")
	}
	must(t, v.ValidatePassword("a new passphrase"))
	must(t, v.Unlock("a new passphrase"))
	got, err := snapshot(v.notes())
	must(t, err)
	if !same(got, want) {
		t.Fatal("rotation changed archive contents")
	}
}

func TestRotatePasswordRejectsInvalidPreconditions(t *testing.T) {
	v := fixture(t)
	before := read(t, v.meta("sealed.age"))
	if err := v.RotatePassword("wrong", "new"); err == nil {
		t.Fatal("wrong current password accepted")
	}
	if !bytes.Equal(before, read(t, v.meta("sealed.age"))) || exists(v.meta("txn")) {
		t.Fatal("failed rotation changed vault state")
	}
	must(t, v.Unlock(testPassword))
	if err := v.RotatePassword(testPassword, "new"); err == nil {
		t.Fatal("rotation accepted an open vault")
	}
}

func TestRotatePasswordRecoversEveryPublicationStage(t *testing.T) {
	for _, point := range []string{"begun", "encrypted", "previous-moved", "committed", "verified-published", "finished"} {
		t.Run(point, func(t *testing.T) {
			v := fixture(t)
			want := populate(t, v)
			must(t, v.Lock(testPassword))
			stop := errors.New("simulated interruption")
			v.hook = func(p string) error {
				if p == point {
					return stop
				}
				return nil
			}
			if err := v.RotatePassword(testPassword, "rotated"); !errors.Is(err, stop) {
				t.Fatalf("expected interruption: %v", err)
			}
			if exists(v.notes()) {
				t.Fatal("rotation opened plaintext notes")
			}
			for _, name := range []string{"journal.json", "next.age", "previous.age"} {
				if b, err := os.ReadFile(v.meta("txn", name)); err == nil && (bytes.Contains(b, []byte(testPassword)) || bytes.Contains(b, []byte("rotated"))) {
					t.Fatalf("password leaked into transaction file %s", name)
				}
			}
			v.hook = nil
			must(t, v.Close())
			reopened, err := Open(v.Root)
			must(t, err)
			*v = *reopened
			must(t, v.RotatePassword(testPassword, "rotated"))
			must(t, v.Unlock("rotated"))
			got, err := snapshot(v.notes())
			must(t, err)
			if !same(got, want) {
				t.Fatal("recovery changed archive contents")
			}
		})
	}
}

func TestBackupRestoreRoundTrip(t *testing.T) {
	root := t.TempDir()
	must(t, os.Mkdir(filepath.Join(root, "Inbox"), 0700))
	must(t, Init(root, "Morning Pages", testPassword))
	v, err := Open(root)
	must(t, err)
	want := populate(t, v)
	must(t, v.Lock(testPassword))
	backup := filepath.Join(t.TempDir(), "vault.age")
	must(t, v.Backup(backup, testPassword))
	if bytes.Contains(read(t, backup), []byte("2026-09-09.md")) {
		t.Fatal("backup leaked a note name")
	}
	must(t, v.Close())

	restoredRoot := t.TempDir()
	must(t, os.Mkdir(filepath.Join(restoredRoot, "Inbox"), 0700))
	put(t, filepath.Join(restoredRoot, ".obsidian", "app.json"), []byte("{}"))
	must(t, Restore(restoredRoot, backup, testPassword))
	if exists(filepath.Join(restoredRoot, ".emergence-restore")) || exists(filepath.Join(restoredRoot, "Morning Pages")) {
		t.Fatal("restore left staging or plaintext notes")
	}
	restored, err := Open(restoredRoot)
	must(t, err)
	defer restored.Close()
	if restored.InboxPath() != "Inbox" {
		t.Fatalf("inbox was not preserved: %q", restored.InboxPath())
	}
	must(t, restored.Unlock(testPassword))
	got, err := snapshot(restored.notes())
	must(t, err)
	if !same(got, want) {
		t.Fatal("restore changed archive contents")
	}
	if string(read(t, filepath.Join(restoredRoot, ".obsidian", "app.json"))) != "{}" {
		t.Fatal("restore overwrote ordinary root files")
	}
}

func TestBackupRestoreRejectsUnsafeStates(t *testing.T) {
	root := t.TempDir()
	must(t, Init(root, "Morning Pages", testPassword))
	v, err := Open(root)
	must(t, err)
	if err := v.Backup(filepath.Join(t.TempDir(), "backup.age"), testPassword); err != nil {
		t.Fatal(err)
	}
	must(t, v.Unlock(testPassword))
	if err := v.Backup(filepath.Join(t.TempDir(), "open.age"), testPassword); err == nil {
		t.Fatal("backup accepted an open vault")
	}
	must(t, v.Lock(testPassword))
	must(t, v.Close())

	backup := filepath.Join(t.TempDir(), "backup.age")
	v, err = Open(root)
	must(t, err)
	must(t, v.Backup(backup, testPassword))
	must(t, v.Close())
	destination := t.TempDir()
	if err := Restore(destination, backup, "wrong"); err == nil {
		t.Fatal("wrong restore password accepted")
	}
	if exists(filepath.Join(destination, metadata)) || exists(filepath.Join(destination, ".emergence-restore")) {
		t.Fatal("failed restore left managed state")
	}
	must(t, os.Mkdir(filepath.Join(destination, "Morning Pages"), 0700))
	if err := Restore(destination, backup, testPassword); err == nil {
		t.Fatal("restore overwrote an existing private folder")
	}
	if exists(filepath.Join(destination, metadata)) {
		t.Fatal("restore published after a collision")
	}
}

func TestTodayCreatesExclusiveEmptyLocalNote(t *testing.T) {
	v := fixture(t)
	must(t, v.Unlock(testPassword))
	fixed := time.Date(2026, time.September, 9, 23, 59, 0, 0, time.Local)
	v.now = func() time.Time { return fixed }
	name, err := v.Today()
	must(t, err)
	if name != "2026-09-09.md" || len(read(t, filepath.Join(v.notes(), name))) != 0 {
		t.Fatalf("unexpected daily note: %q", name)
	}
	before := read(t, filepath.Join(v.notes(), name))
	if _, err := v.Today(); err == nil {
		t.Fatal("duplicate daily note accepted")
	}
	if !bytes.Equal(before, read(t, filepath.Join(v.notes(), name))) {
		t.Fatal("duplicate daily note changed existing content")
	}
	put(t, filepath.Join(v.notes(), "2026-09-10.MD"), []byte("existing"))
	v.now = func() time.Time { return fixed.Add(24 * time.Hour) }
	if _, err := v.Today(); err == nil {
		t.Fatal("case-insensitive collision accepted")
	}
}

func TestTodayRejectsLockedOrIncompleteVault(t *testing.T) {
	v := fixture(t)
	if _, err := v.Today(); err == nil {
		t.Fatal("today accepted a locked vault")
	}
	must(t, v.Unlock(testPassword))
	must(t, os.Mkdir(v.meta("txn"), 0700))
	if _, err := v.Today(); err == nil {
		t.Fatal("today accepted an incomplete vault")
	}
}

func TestInitRejectsExistingFolder(t *testing.T) {
	root := t.TempDir()
	must(t, os.Mkdir(filepath.Join(root, "Morning Pages"), 0700))
	if err := Init(root, "Morning Pages", testPassword); err == nil {
		t.Fatal("existing folder accepted")
	}
	if exists(filepath.Join(root, metadata)) {
		t.Fatal("partial init")
	}
	for _, name := range []string{"../escape", ".obsidian", "a/b", "CON", "C:\\notes"} {
		if err := Init(root, name, testPassword); err == nil {
			t.Fatal(name)
		}
	}
}

func maliciousArchive(t *testing.T, path string, headers []*tar.Header) {
	t.Helper()
	var b bytes.Buffer
	r, err := newRecipient(testPassword)
	must(t, err)
	w, err := age.Encrypt(&b, r)
	must(t, err)
	tw := tar.NewWriter(w)
	for _, h := range headers {
		must(t, tw.WriteHeader(h))
		if h.Size > 0 {
			_, err = io.CopyN(tw, strings.NewReader("payload"), h.Size)
			must(t, err)
		}
	}
	must(t, tw.Close())
	must(t, w.Close())
	put(t, path, b.Bytes())
}

func TestRejectMaliciousArchives(t *testing.T) {
	for _, name := range []string{"../escape", "/absolute", "C:/escape", "a\\b", "CON", "note.", "ok/../../escape"} {
		t.Run(name, func(t *testing.T) {
			v := fixture(t)
			maliciousArchive(t, v.meta("sealed.age"), []*tar.Header{{Name: name, Typeflag: tar.TypeReg, Mode: 0600, Size: 7}})
			if err := v.Unlock(testPassword); err == nil {
				t.Fatal("unsafe archive accepted")
			}
			if exists(v.notes()) {
				t.Fatal("published unsafe archive")
			}
		})
	}
	for _, kind := range []byte{tar.TypeSymlink, tar.TypeLink, tar.TypeFifo} {
		v := fixture(t)
		maliciousArchive(t, v.meta("sealed.age"), []*tar.Header{{Name: "link", Typeflag: kind, Linkname: "../outside"}})
		if err := v.Unlock(testPassword); err == nil {
			t.Fatal("link accepted")
		}
	}
	v := fixture(t)
	maliciousArchive(t, v.meta("sealed.age"), []*tar.Header{{Name: "Note", Typeflag: tar.TypeReg}, {Name: "note", Typeflag: tar.TypeReg}})
	if err := v.Unlock(testPassword); err == nil {
		t.Fatal("case collision accepted")
	}
}

func TestSymlinkRejected(t *testing.T) {
	v := fixture(t)
	must(t, v.Unlock(testPassword))
	outside := filepath.Join(t.TempDir(), "private")
	put(t, outside, []byte("outside"))
	if err := os.Symlink(outside, filepath.Join(v.notes(), "link")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := v.Lock(testPassword); err == nil {
		t.Fatal("symlink accepted")
	}
	if string(read(t, outside)) != "outside" {
		t.Fatal("outside data changed")
	}
}

func TestOppositeCommandCannotResume(t *testing.T) {
	v := fixture(t)
	populate(t, v)
	v.hook = func(p string) error {
		if p == "encrypted" {
			return errors.New("stop")
		}
		return nil
	}
	if err := v.Lock(testPassword); err == nil {
		t.Fatal("expected stop")
	}
	v.hook = nil
	if err := v.Unlock(testPassword); err == nil {
		t.Fatal("opposite command accepted")
	}
	must(t, v.Lock(testPassword))
}

func TestDestroyRemovesOpenPrivateState(t *testing.T) {
	v := fixture(t)
	must(t, v.Unlock(testPassword))
	put(t, filepath.Join(v.notes(), "morning.md"), []byte("private"))
	put(t, filepath.Join(v.notes(), "attachment.bin"), []byte{1, 2, 3})
	targets, err := v.DestroyTargets()
	must(t, err)
	if len(targets) != 2 || targets[0] != v.notes() || targets[1] != v.meta() {
		t.Fatalf("unexpected targets: %#v", targets)
	}
	must(t, v.Destroy(testPassword))
	if exists(v.notes()) || exists(v.meta()) || exists(filepath.Join(v.Root, ".emergence-destroy")) {
		t.Fatal("destroy left a managed target behind")
	}
	if _, err := Open(v.Root); err == nil {
		t.Fatal("destroyed vault can still be opened")
	}
}

func TestDestroyAuthenticatesBeforeRemoval(t *testing.T) {
	v := fixture(t)
	must(t, v.Unlock(testPassword))
	put(t, filepath.Join(v.notes(), "morning.md"), []byte("private"))
	if err := v.Destroy("wrong"); err == nil {
		t.Fatal("wrong password accepted")
	}
	if !exists(v.notes()) || !exists(v.meta()) || exists(v.destroyMarker()) {
		t.Fatal("failed authentication changed destroy targets")
	}
	if err := v.ValidateDestroyCwd(v.notes()); err == nil {
		t.Fatal("destroy allowed from inside private folder")
	}
}

func TestDestroyResumesFromMarker(t *testing.T) {
	v := fixture(t)
	must(t, v.Unlock(testPassword))
	put(t, filepath.Join(v.notes(), "morning.md"), []byte("private"))
	stop := errors.New("simulated interruption")
	v.hook = func(point string) error {
		if point == "destroy-notes" {
			return stop
		}
		return nil
	}
	if err := v.Destroy(testPassword); !errors.Is(err, stop) {
		t.Fatalf("expected interruption: %v", err)
	}
	if exists(v.notes()) == true || !exists(v.meta()) || !exists(v.destroyMarker()) {
		t.Fatal("interrupted destroy did not leave resumable state")
	}
	v.hook = nil
	must(t, v.Destroy(testPassword))
	if exists(v.meta()) || exists(v.destroyMarker()) {
		t.Fatal("resumed destroy left metadata")
	}
}

func TestDestroyRejectsInvalidArchive(t *testing.T) {
	v := fixture(t)
	sealed := v.meta("sealed.age")
	backup := sealed + ".backup"
	must(t, os.Rename(sealed, backup))
	must(t, os.Mkdir(sealed, 0700))
	if _, err := v.DestroyTargets(); err == nil {
		t.Fatal("directory accepted as sealed.age")
	}
	must(t, os.Remove(sealed))
	must(t, os.Rename(backup, sealed))
}

func TestInitAndSelectInbox(t *testing.T) {
	root := t.TempDir()
	must(t, os.Mkdir(filepath.Join(root, "Inbox"), 0700))
	must(t, Init(root, "Morning Pages", testPassword))
	v, err := Open(root)
	must(t, err)
	defer v.Close()
	if v.InboxPath() != "Inbox" {
		t.Fatalf("expected automatic Inbox, got %q", v.InboxPath())
	}

	other := t.TempDir()
	must(t, os.Mkdir(filepath.Join(other, "Inbox A"), 0700))
	must(t, os.Mkdir(filepath.Join(other, "myINBOX"), 0700))
	must(t, Init(other, "Morning Pages", testPassword))
	w, err := Open(other)
	must(t, err)
	defer w.Close()
	if w.InboxPath() != "" {
		t.Fatalf("multiple Inboxes should require selection, got %q", w.InboxPath())
	}
	candidates, err := w.InboxCandidates()
	must(t, err)
	if len(candidates) != 2 {
		t.Fatalf("unexpected candidates: %#v", candidates)
	}
	must(t, w.SetInbox(candidates[1]))
	if w.InboxPath() != candidates[1] {
		t.Fatalf("selection was not persisted: %q", w.InboxPath())
	}
}

func TestReviewDeletesAndMovesMarkdown(t *testing.T) {
	root := t.TempDir()
	must(t, os.Mkdir(filepath.Join(root, "Inbox"), 0700))
	must(t, Init(root, "Morning Pages", testPassword))
	v, err := Open(root)
	must(t, err)
	defer v.Close()
	must(t, v.Unlock(testPassword))
	put(t, filepath.Join(v.notes(), "keep.md"), []byte("keep"))
	put(t, filepath.Join(v.notes(), "delete.md"), []byte("delete"))
	put(t, filepath.Join(v.notes(), "nested", "move.MD"), []byte("nested"))
	put(t, filepath.Join(v.notes(), "attachment.bin"), []byte{1, 2, 3})
	must(t, v.Review([]string{"delete.md"}))
	if exists(filepath.Join(v.notes(), "delete.md")) {
		t.Fatal("selected note was not deleted")
	}
	if !exists(filepath.Join(root, "Inbox", "keep.md")) || !exists(filepath.Join(root, "Inbox", "move.MD")) {
		t.Fatal("kept notes were not moved to Inbox")
	}
	if !exists(filepath.Join(v.notes(), "attachment.bin")) {
		t.Fatal("non-Markdown attachment was moved")
	}
	must(t, v.Lock(testPassword))
	must(t, v.Unlock(testPassword))
	if !exists(filepath.Join(v.notes(), "attachment.bin")) {
		t.Fatal("remaining non-Markdown file was not preserved")
	}
}

func TestReviewPlanDoesNotMutate(t *testing.T) {
	root := t.TempDir()
	must(t, os.Mkdir(filepath.Join(root, "Inbox"), 0700))
	must(t, Init(root, "Morning Pages", testPassword))
	v, err := Open(root)
	must(t, err)
	defer v.Close()
	must(t, v.Unlock(testPassword))
	put(t, filepath.Join(v.notes(), "keep.md"), []byte("keep"))
	put(t, filepath.Join(v.notes(), "delete.md"), []byte("delete"))
	beforeKeep := read(t, filepath.Join(v.notes(), "keep.md"))
	beforeDelete := read(t, filepath.Join(v.notes(), "delete.md"))
	plan, err := v.ReviewPlan([]string{"delete.md"})
	must(t, err)
	if len(plan) != 2 || plan[0].Action != "delete" || plan[1].Action != "move" {
		t.Fatalf("unexpected dry-run plan: %#v", plan)
	}
	if exists(v.meta("txn")) || exists(filepath.Join(root, "Inbox", "keep.md")) || !bytes.Equal(beforeKeep, read(t, filepath.Join(v.notes(), "keep.md"))) || !bytes.Equal(beforeDelete, read(t, filepath.Join(v.notes(), "delete.md"))) {
		t.Fatal("dry-run changed vault state")
	}
}

func TestReviewFiltersByDateSizeAndPath(t *testing.T) {
	root := t.TempDir()
	must(t, os.Mkdir(filepath.Join(root, "Inbox"), 0700))
	must(t, Init(root, "Morning Pages", testPassword))
	v, err := Open(root)
	must(t, err)
	defer v.Close()
	must(t, v.Unlock(testPassword))
	put(t, filepath.Join(v.notes(), "old.md"), []byte("old"))
	put(t, filepath.Join(v.notes(), "recent.md"), []byte("recent!"))
	put(t, filepath.Join(v.notes(), "nested", "inside.md"), []byte("inside"))
	put(t, filepath.Join(v.notes(), "nested", "skip.txt"), []byte("skip"))
	old := time.Date(2026, time.September, 1, 12, 0, 0, 0, time.Local)
	recent := time.Date(2026, time.September, 9, 12, 0, 0, 0, time.Local)
	must(t, os.Chtimes(filepath.Join(v.notes(), "old.md"), old, old))
	must(t, os.Chtimes(filepath.Join(v.notes(), "recent.md"), recent, recent))
	must(t, os.Chtimes(filepath.Join(v.notes(), "nested", "inside.md"), recent, recent))
	min := int64(7)
	info, err := v.MarkdownNoteInfo(ReviewFilter{After: &recent, MinSize: &min})
	must(t, err)
	if len(info) != 1 || info[0].Name != "recent.md" {
		t.Fatalf("unexpected date/size filter result: %#v", info)
	}
	prefix := ReviewFilter{PathPrefix: "nested"}
	info, err = v.MarkdownNoteInfo(prefix)
	must(t, err)
	if len(info) != 1 || info[0].Name != "nested/inside.md" {
		t.Fatalf("unexpected path filter result: %#v", info)
	}
	before := recent
	info, err = v.MarkdownNoteInfo(ReviewFilter{Before: &before})
	must(t, err)
	if len(info) != 1 || info[0].Name != "old.md" {
		t.Fatalf("before boundary is not exclusive: %#v", info)
	}
	if err := (ReviewFilter{PathPrefix: "../escape"}).Validate(); err == nil {
		t.Fatal("unsafe path prefix accepted")
	}
	if err := (ReviewFilter{MinSize: &min, MaxSize: func() *int64 { n := int64(2); return &n }()}).Validate(); err == nil {
		t.Fatal("inverted size range accepted")
	}
}

func TestReviewFilterOnlyMovesSelectedSet(t *testing.T) {
	root := t.TempDir()
	must(t, os.Mkdir(filepath.Join(root, "Inbox"), 0700))
	must(t, Init(root, "Morning Pages", testPassword))
	v, err := Open(root)
	must(t, err)
	defer v.Close()
	must(t, v.Unlock(testPassword))
	put(t, filepath.Join(v.notes(), "old.md"), []byte("old"))
	put(t, filepath.Join(v.notes(), "nested", "recent.md"), []byte("recent"))
	cutoff := time.Now().In(time.Local).Add(-time.Hour)
	now := time.Now().In(time.Local)
	must(t, os.Chtimes(filepath.Join(v.notes(), "old.md"), cutoff.Add(-time.Hour), cutoff.Add(-time.Hour)))
	must(t, os.Chtimes(filepath.Join(v.notes(), "nested", "recent.md"), now, now))
	must(t, v.ReviewWithFilter(nil, ReviewFilter{After: &cutoff}))
	if !exists(filepath.Join(v.notes(), "old.md")) || !exists(filepath.Join(root, "Inbox", "recent.md")) {
		t.Fatal("review filter changed a note outside the selected set")
	}
}

func TestReviewCollisionsPreserveNotes(t *testing.T) {
	root := t.TempDir()
	must(t, os.Mkdir(filepath.Join(root, "Inbox"), 0700))
	must(t, Init(root, "Morning Pages", testPassword))
	v, err := Open(root)
	must(t, err)
	defer v.Close()
	must(t, v.Unlock(testPassword))
	put(t, filepath.Join(v.notes(), "keep.md"), []byte("private"))
	put(t, filepath.Join(root, "Inbox", "keep.md"), []byte("existing"))
	if err := v.Review(nil); err == nil {
		t.Fatal("Inbox collision accepted")
	}
	if string(read(t, filepath.Join(v.notes(), "keep.md"))) != "private" || string(read(t, filepath.Join(root, "Inbox", "keep.md"))) != "existing" {
		t.Fatal("collision changed files")
	}
}

func TestReviewResumesJournal(t *testing.T) {
	root := t.TempDir()
	must(t, os.Mkdir(filepath.Join(root, "Inbox"), 0700))
	must(t, Init(root, "Morning Pages", testPassword))
	v, err := Open(root)
	must(t, err)
	defer v.Close()
	must(t, v.Unlock(testPassword))
	put(t, filepath.Join(v.notes(), "delete.md"), []byte("delete"))
	put(t, filepath.Join(v.notes(), "keep.md"), []byte("keep"))
	stop := errors.New("simulated interruption")
	v.hook = func(point string) error {
		if point == "review-deleted" {
			return stop
		}
		return nil
	}
	if err := v.Review([]string{"delete.md"}); !errors.Is(err, stop) {
		t.Fatalf("expected interruption: %v", err)
	}
	if !v.ReviewInProgress() || exists(filepath.Join(v.notes(), "delete.md")) == true {
		t.Fatal("review journal did not preserve resumable state")
	}
	v.hook = nil
	must(t, v.Review(nil))
	if exists(v.meta("txn")) || exists(filepath.Join(v.notes(), "keep.md")) || !exists(filepath.Join(root, "Inbox", "keep.md")) {
		t.Fatal("review recovery did not finish")
	}
}
