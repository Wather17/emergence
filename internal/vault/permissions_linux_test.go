package vault

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPermissionFailuresPreserveNotes(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses permission checks")
	}
	for _, phase := range []string{"write", "remove"} {
		t.Run(phase, func(t *testing.T) {
			v := fixture(t)
			want := populate(t, v)
			path := v.meta()
			if phase == "remove" {
				path = v.notes()
			}
			must(t, os.Chmod(path, 0500))
			t.Cleanup(func() { _ = os.Chmod(path, 0700) })
			if err := v.Lock(testPassword); err == nil {
				t.Fatal("expected permission failure")
			}
			// A removal failure may have removed other verified files already;
			// the original complete snapshot must remain decryptable.
			if phase == "write" {
				got, err := snapshot(v.notes())
				must(t, err)
				if !same(want, got) {
					t.Fatal("write failure changed notes")
				}
			} else {
				got, err := decrypt(v.meta("sealed.age"), testPassword, "")
				must(t, err)
				if !same(want, got) {
					t.Fatal("missing committed snapshot")
				}
			}
			must(t, os.Chmod(path, 0700))
			must(t, v.Lock(testPassword))
			must(t, v.Unlock(testPassword))
			got, err := snapshot(v.notes())
			must(t, err)
			if !same(want, got) {
				t.Fatal("recovery lost files")
			}
		})
	}
}

func TestPrivatePermissions(t *testing.T) {
	v := fixture(t)
	populate(t, v)
	for _, p := range []string{v.meta(), v.meta("sealed.age"), v.notes(), filepath.Join(v.notes(), "2026-09-09.md")} {
		i, err := os.Stat(p)
		must(t, err)
		if i.Mode().Perm()&0077 != 0 {
			t.Fatalf("permissions too broad: %s %v", p, i.Mode())
		}
	}
}
