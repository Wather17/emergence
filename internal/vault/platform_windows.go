package vault

import (
	"fmt"
	"golang.org/x/sys/windows"
)

func rejectReparse(path string) error {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	attrs, err := windows.GetFileAttributes(p)
	if err != nil {
		return err
	}
	if attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return fmt.Errorf("pontos de redirecionamento não são aceitos: %s", path)
	}
	return nil
}

// File contents are flushed explicitly. Windows does not expose portable
// directory fsync semantics; recovery must also inspect actual file presence.
func syncDir(string) error { return nil }
