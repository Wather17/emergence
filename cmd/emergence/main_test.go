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
	for _, args := range [][]string{{"unknown"}, {"lock", "extra"}, {"unlock", "--password", "secret"}, {"destroy", "--force"}, {"init", "--bad"}} {
		if err := run(args, &bytes.Buffer{}, func(string) (string, error) { t.Fatal("unexpected password prompt"); return "", nil }); err == nil {
			t.Fatal("invalid arguments accepted")
		}
	}
}

func TestDestroyRequiresInteractiveTerminal(t *testing.T) {
	t.Chdir(t.TempDir())
	err := run([]string{"destroy"}, &bytes.Buffer{}, func(string) (string, error) {
		t.Fatal("destroy requested input without a terminal")
		return "", nil
	})
	if err == nil || !strings.Contains(err.Error(), "terminal interativo") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReviewSelectionParsing(t *testing.T) {
	notes := []string{"a.md", "nested/b.md", "c.MD"}
	selected, canceled, err := parseSelection("1, 3", notes)
	if err != nil || canceled || strings.Join(selected, ",") != "a.md,c.MD" {
		t.Fatalf("unexpected selection: %#v %v %v", selected, canceled, err)
	}
	if _, canceled, err := parseSelection("cancelar", notes); err != nil || !canceled {
		t.Fatalf("cancel was not recognized: %v %v", canceled, err)
	}
	if _, _, err := parseSelection("0", notes); err == nil {
		t.Fatal("zero selection accepted")
	}
	if confirmed("sim") != true || confirmed("não") {
		t.Fatal("confirmation parser incorrect")
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

func TestVersionWithoutVaultOrPassword(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, arg := range []string{"version", "--version"} {
		var out bytes.Buffer
		if err := run([]string{arg}, &out, func(string) (string, error) { t.Fatal("unexpected password prompt"); return "", nil }); err != nil {
			t.Fatal(err)
		}
		if out.String() != "emergence "+version+" ("+commit+")\n" {
			t.Fatal(out.String())
		}
	}
}
