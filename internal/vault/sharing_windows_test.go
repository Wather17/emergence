package vault

import (
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestOpenWindowsFilePreventsDeletion(t *testing.T) {
	v := fixture(t)
	want := populate(t, v)
	p, err := windows.UTF16PtrFromString(filepath.Join(v.notes(), "2026-09-09.md"))
	must(t, err)
	h, err := windows.CreateFile(p, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	must(t, err)
	defer func() {
		if h != windows.InvalidHandle {
			_ = windows.CloseHandle(h)
		}
	}()
	if err := v.Lock(testPassword); err == nil {
		t.Fatal("expected sharing violation")
	}
	got, err := decrypt(v.meta("sealed.age"), testPassword, "")
	must(t, err)
	if !same(want, got) {
		t.Fatal("snapshot missing")
	}
	must(t, windows.CloseHandle(h))
	h = windows.InvalidHandle
	must(t, v.Lock(testPassword))
	must(t, v.Unlock(testPassword))
	got, err = snapshot(v.notes())
	must(t, err)
	if !same(want, got) {
		t.Fatal("sharing recovery lost files")
	}
}
