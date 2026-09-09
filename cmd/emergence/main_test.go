package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestUsageAndValidation(t *testing.T) {
	for _, args := range [][]string{nil, {"help"}, {"--help"}, {"init", "-h"}} {
		var out bytes.Buffer
		if err := run(args, &out, func(string) (string, error) { t.Fatal("unexpected password prompt"); return "", nil }); err != nil {
			t.Fatal(err)
		}
		if out.Len() == 0 {
			t.Fatal("missing help")
		}
	}
	for _, args := range [][]string{{"unknown"}, {"lock", "extra"}, {"unlock", "--password", "secret"}, {"init", "--bad"}} {
		if err := run(args, &bytes.Buffer{}, func(string) (string, error) { t.Fatal("unexpected password prompt"); return "", nil }); err == nil {
			t.Fatal("invalid arguments accepted")
		}
	}
}

func TestPasswordConfirmation(t *testing.T) {
	t.Chdir(t.TempDir())
	n := 0
	err := run([]string{"init"}, &bytes.Buffer{}, func(string) (string, error) { n++; return strings.Repeat("x", n), nil })
	if err == nil || !strings.Contains(err.Error(), "não coincidem") {
		t.Fatal(err)
	}
}
