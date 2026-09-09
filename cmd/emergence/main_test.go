package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"emergence/internal/vault"
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

func TestDoctorDoesNotPromptByDefault(t *testing.T) {
	root := t.TempDir()
	if err := vault.Init(root, "Morning Pages", "test password"); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	var out bytes.Buffer
	if err := run([]string{"doctor"}, &out, func(string) (string, error) {
		t.Fatal("doctor requested a password without --check-archive")
		return "", nil
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Diagnóstico da vault") || !strings.Contains(out.String(), "[OK]") {
		t.Fatalf("unexpected doctor output: %s", out.String())
	}
}

func TestRotatePasswordPromptsAndPublishes(t *testing.T) {
	root := t.TempDir()
	if err := vault.Init(root, "Morning Pages", "old password"); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	answers := []string{"old password", "new password", "new password"}
	index := 0
	var out bytes.Buffer
	if err := run([]string{"rotate-password"}, &out, func(string) (string, error) {
		answer := answers[index]
		index++
		return answer, nil
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "rotacionada") {
		t.Fatalf("unexpected rotation output: %s", out.String())
	}
	v, err := vault.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	if err := v.ValidatePassword("old password"); err == nil {
		t.Fatal("old password still accepted")
	}
	if err := v.ValidatePassword("new password"); err != nil {
		t.Fatal(err)
	}
}

func TestBackupAndRestoreCommands(t *testing.T) {
	root := t.TempDir()
	if err := vault.Init(root, "Morning Pages", "backup password"); err != nil {
		t.Fatal(err)
	}
	backup := root + "-bundle.age"
	t.Chdir(root)
	if err := run([]string{"backup", "--output", backup}, &bytes.Buffer{}, func(string) (string, error) {
		return "backup password", nil
	}); err != nil {
		t.Fatal(err)
	}
	destination := t.TempDir()
	t.Chdir(destination)
	if err := run([]string{"restore", "--input", backup}, &bytes.Buffer{}, func(string) (string, error) {
		return "backup password", nil
	}); err != nil {
		t.Fatal(err)
	}
	v, err := vault.Open(destination)
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	if err := v.ValidatePassword("backup password"); err != nil {
		t.Fatal(err)
	}
}

func TestReviewDryRunCommandDoesNotMutate(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "Inbox"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := vault.Init(root, "Morning Pages", "review password"); err != nil {
		t.Fatal(err)
	}
	v, err := vault.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Unlock("review password"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Morning Pages", "keep.md"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := v.Close(); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	answers := []string{"1"}
	index := 0
	var out bytes.Buffer
	if err := run([]string{"review", "--dry-run"}, &out, func(string) (string, error) {
		answer := answers[index]
		index++
		return answer, nil
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "dry-run") || !strings.Contains(out.String(), "Nenhum arquivo foi alterado") {
		t.Fatalf("unexpected dry-run output: %s", out.String())
	}
	if _, err := os.Stat(filepath.Join(root, "Morning Pages", "keep.md")); err != nil {
		t.Fatal("dry-run moved the note")
	}
}

func TestTodayCommandDoesNotPrompt(t *testing.T) {
	root := t.TempDir()
	if err := vault.Init(root, "Morning Pages", "today password"); err != nil {
		t.Fatal(err)
	}
	v, err := vault.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Unlock("today password"); err != nil {
		t.Fatal(err)
	}
	if err := v.Close(); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	var out bytes.Buffer
	if err := run([]string{"today"}, &out, func(string) (string, error) {
		t.Fatal("today requested a password")
		return "", nil
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Nota diária criada:") {
		t.Fatalf("unexpected today output: %s", out.String())
	}
}
