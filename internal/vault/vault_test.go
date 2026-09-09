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
