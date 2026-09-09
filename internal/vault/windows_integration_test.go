//go:build windows

package vault

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func windowsAttributes(t *testing.T, path string) uint32 {
	t.Helper()
	p, err := windows.UTF16PtrFromString(path)
	must(t, err)
	attrs, err := windows.GetFileAttributes(p)
	must(t, err)
	return attrs
}

func setWindowsAttributes(t *testing.T, path string, attrs uint32) {
	t.Helper()
	p, err := windows.UTF16PtrFromString(path)
	must(t, err)
	must(t, windows.SetFileAttributes(p, attrs))
}

func TestWindowsJunctionIsRejectedWithoutFollowingTarget(t *testing.T) {
	v := fixture(t)
	must(t, v.Unlock(testPassword))
	external := t.TempDir()
	marker := filepath.Join(external, "outside.txt")
	put(t, marker, []byte("must remain untouched"))
	junction := filepath.Join(v.notes(), "external")
	cmd := exec.Command("cmd.exe", "/c", "mklink", "/J", junction, external)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("mklink /J failed: %v (%s)", err, output)
	}
	defer os.Remove(junction)
	if err := v.Lock(testPassword); err == nil {
		t.Fatal("lock followed a junction")
	}
	if string(read(t, marker)) != "must remain untouched" {
		t.Fatal("lock touched data outside the vault")
	}
	if !exists(junction) || exists(v.meta("txn")) {
		t.Fatal("junction rejection left an unsafe transaction state")
	}
	must(t, os.Remove(junction))
	must(t, v.Lock(testPassword))
	must(t, v.Unlock(testPassword))
}

func TestWindowsReadOnlyAndDeniedNotePreservesAndRecovers(t *testing.T) {
	v := fixture(t)
	must(t, v.Unlock(testPassword))
	note := filepath.Join(v.notes(), "readonly.md")
	want := []byte("preserve this note")
	put(t, note, want)
	original := windowsAttributes(t, note)
	defer setWindowsAttributes(t, note, original)
	setWindowsAttributes(t, note, original|windows.FILE_ATTRIBUTE_READONLY)
	if err := os.WriteFile(note, []byte("must fail"), 0600); err == nil {
		t.Fatal("read-only attribute allowed an overwrite")
	}
	if string(read(t, note)) != string(want) {
		t.Fatal("read-only overwrite changed the note")
	}
	setWindowsAttributes(t, note, original)
	user := os.Getenv("USERNAME")
	if user == "" {
		t.Fatal("USERNAME is unavailable; cannot create an ACL denial fixture")
	}
	icacls := func(args ...string) {
		t.Helper()
		cmd := exec.Command("icacls.exe", args...)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("icacls %v failed: %v (%s)", args, err, output)
		}
	}
	icacls(note, "/deny", user+":(D)")
	defer func() { _ = exec.Command("icacls.exe", note, "/remove:d", user).Run() }()
	if err := v.Lock(testPassword); err == nil {
		t.Fatal("lock ignored an ACL denial")
	}
	icacls(note, "/remove:d", user)
	must(t, v.Lock(testPassword))
	must(t, v.Unlock(testPassword))
	if string(read(t, note)) != string(want) {
		t.Fatal("recovery after restoring attributes changed the note")
	}
}

func TestWindowsCaseInsensitiveInboxCollisionIsDeterministic(t *testing.T) {
	root := t.TempDir()
	must(t, os.Mkdir(filepath.Join(root, "Inbox"), 0700))
	must(t, Init(root, "Morning Pages", testPassword))
	v, err := Open(root)
	must(t, err)
	defer v.Close()
	must(t, v.Unlock(testPassword))
	put(t, filepath.Join(v.notes(), "idea.md"), []byte("private"))
	put(t, filepath.Join(root, "Inbox", "IDEA.MD"), []byte("existing"))
	if err := v.Review(nil); err == nil {
		t.Fatal("review accepted a case-insensitive Inbox collision")
	}
	if string(read(t, filepath.Join(v.notes(), "idea.md"))) != "private" || string(read(t, filepath.Join(root, "Inbox", "IDEA.MD"))) != "existing" {
		t.Fatal("collision changed one of the notes")
	}
}
