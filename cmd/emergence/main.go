package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"emergence/internal/vault"
	"golang.org/x/term"
)

var version = "dev"
var commit = "unknown"

const usage = `Emergence — notas privadas na sua vault

Uso:
  emergence init [--folder "Morning Pages"]
  emergence unlock
  emergence lock
  emergence rotate-password
  emergence status
  emergence doctor [--check-archive]
  emergence inbox
  emergence review
  emergence destroy
  emergence version
  emergence help

Execute init na raiz da vault. Os demais comandos também funcionam em subpastas.
Encerre a edição antes de lock. A senha é solicitada no terminal, sem exibição.
destroy exige um terminal interativo, valida a senha e a frase literal
DESTROY <nome-da-pasta-privada> antes de remover a pasta privada e .emergence.
`

func password(prompt string) (string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", errors.New("a senha deve ser digitada em um terminal interativo")
	}
	fmt.Fprint(os.Stderr, prompt)
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	s := string(b)
	clear(b)
	if s == "" {
		return "", errors.New("a senha não pode ser vazia")
	}
	return s, nil
}

func parseSelection(raw string, notes []string) ([]string, bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, false, nil
	}
	switch strings.ToLower(raw) {
	case "c", "cancel", "cancelar", "q", "quit":
		return nil, true, nil
	}
	fields := strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' })
	selected := make([]string, 0, len(fields))
	seen := map[int]bool{}
	for _, field := range fields {
		n, err := strconv.Atoi(field)
		if err != nil || n < 1 || n > len(notes) {
			return nil, false, fmt.Errorf("seleção inválida: %s", field)
		}
		if seen[n] {
			return nil, false, fmt.Errorf("nota selecionada mais de uma vez: %d", n)
		}
		seen[n] = true
		selected = append(selected, notes[n-1])
	}
	return selected, false, nil
}

func confirmed(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "s", "sim", "y", "yes":
		return true
	default:
		return false
	}
}

func run(args []string, out io.Writer, ask func(string) (string, error)) error {
	if len(args) == 1 && (args[0] == "version" || args[0] == "--version") {
		fmt.Fprintf(out, "emergence %s (%s)\n", version, commit)
		return nil
	}
	if len(args) == 0 {
		fmt.Fprint(out, usage)
		return nil
	}
	if args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprint(out, usage)
		return nil
	}
	command := args[0]
	if command != "init" && command != "unlock" && command != "lock" && command != "rotate-password" && command != "status" && command != "doctor" && command != "inbox" && command != "review" && command != "destroy" {
		return fmt.Errorf("comando desconhecido: %s; use emergence help", command)
	}
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(out)
	folder := "Morning Pages"
	if command == "init" {
		fs.StringVar(&folder, "folder", folder, "nome da pasta privada")
	}
	checkArchive := false
	if command == "doctor" {
		fs.BoolVar(&checkArchive, "check-archive", false, "autenticar e validar integralmente sealed.age")
	}
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("argumentos inesperados; use emergence help")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	if command == "destroy" && !term.IsTerminal(int(os.Stdin.Fd())) {
		return errors.New("destroy exige um terminal interativo")
	}
	if command == "init" {
		p, err := ask("Crie uma senha: ")
		if err != nil {
			return err
		}
		confirm, err := ask("Confirme a senha: ")
		if err != nil {
			return err
		}
		if p != confirm {
			return errors.New("as senhas não coincidem")
		}
		if err := vault.Init(cwd, folder, p); err != nil {
			return err
		}
		fmt.Fprintf(out, "Vault inicializada e trancada. Use emergence unlock para abrir %q.\n", folder)
		candidates, err := vault.FindInboxCandidates(cwd, folder)
		if err == nil {
			switch len(candidates) {
			case 0:
				fmt.Fprintln(out, "Nenhuma pasta Inbox encontrada; review ficará disponível após executar emergence inbox.")
			case 1:
				fmt.Fprintf(out, "Inbox padrão configurada: %s\n", candidates[0])
			default:
				fmt.Fprintln(out, "Várias pastas Inbox encontradas; execute emergence inbox para escolher a padrão.")
			}
		}
		return nil
	}
	if command == "doctor" {
		var p string
		if checkArchive {
			if !term.IsTerminal(int(os.Stdin.Fd())) {
				return errors.New("doctor --check-archive exige um terminal interativo")
			}
			p, err = ask("Senha para verificar sealed.age: ")
			if err != nil {
				return err
			}
		}
		report, err := vault.Doctor(cwd, checkArchive, p)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "Diagnóstico da vault: %s\n", report.Root)
		for _, check := range report.Checks {
			fmt.Fprintf(out, "[%s] %s", check.Severity, check.Name)
			if check.Path != "" {
				fmt.Fprintf(out, " (%s)", check.Path)
			}
			fmt.Fprintf(out, ": %s", check.Message)
			if check.Action != "" {
				fmt.Fprintf(out, " — %s", check.Action)
			}
			fmt.Fprintln(out)
		}
		if report.HasErrors() {
			return errors.New("doctor encontrou problemas que exigem intervenção")
		}
		return nil
	}
	v, err := vault.Open(cwd)
	if err != nil {
		return err
	}
	defer v.Close()
	if command == "rotate-password" {
		current, err := ask("Senha atual: ")
		if err != nil {
			return err
		}
		next, err := ask("Nova senha: ")
		if err != nil {
			return err
		}
		confirm, err := ask("Confirme a nova senha: ")
		if err != nil {
			return err
		}
		if next != confirm {
			return errors.New("as senhas não coincidem")
		}
		if err := v.RotatePassword(current, next); err != nil {
			return err
		}
		fmt.Fprintln(out, "Senha rotacionada com sucesso. Use a nova senha no próximo unlock.")
		return nil
	}
	if command == "inbox" {
		candidates, err := v.InboxCandidates()
		if err != nil {
			return err
		}
		if len(candidates) == 0 {
			fmt.Fprintln(out, "Nenhuma pasta Inbox encontrada na vault.")
			return nil
		}
		fmt.Fprintln(out, "Pastas Inbox disponíveis:")
		for i, candidate := range candidates {
			fmt.Fprintf(out, "  %d) %s\n", i+1, candidate)
		}
		answer, err := ask("Escolha o número da Inbox (ou cancelar): ")
		if err != nil {
			return err
		}
		if strings.EqualFold(strings.TrimSpace(answer), "cancelar") || strings.EqualFold(strings.TrimSpace(answer), "cancel") || strings.EqualFold(strings.TrimSpace(answer), "c") {
			fmt.Fprintln(out, "Seleção cancelada.")
			return nil
		}
		n, err := strconv.Atoi(strings.TrimSpace(answer))
		if err != nil || n < 1 || n > len(candidates) {
			return errors.New("seleção de Inbox inválida")
		}
		if err := v.SetInbox(candidates[n-1]); err != nil {
			return err
		}
		fmt.Fprintf(out, "Inbox padrão configurada: %s\n", v.InboxPath())
		return nil
	}
	if command == "review" {
		if v.ReviewInProgress() {
			fmt.Fprintln(out, "Retomando revisão incompleta...")
			if err := v.Review(nil); err != nil {
				return err
			}
			fmt.Fprintln(out, "Revisão concluída.")
			return nil
		}
		notes, err := v.MarkdownNotes()
		if err != nil {
			return err
		}
		if len(notes) == 0 {
			fmt.Fprintln(out, "Nenhuma nota Markdown na pasta privada.")
			return nil
		}
		fmt.Fprintln(out, "Notas Markdown:")
		for i, note := range notes {
			fmt.Fprintf(out, "  %d) %s\n", i+1, note)
		}
		answer, err := ask("Números para apagar (vazio mantém todas; cancelar aborta): ")
		if err != nil {
			return err
		}
		selected, canceled, err := parseSelection(answer, notes)
		if err != nil {
			return err
		}
		if canceled {
			fmt.Fprintln(out, "Revisão cancelada.")
			return nil
		}
		if len(selected) > 0 {
			fmt.Fprintln(out, "Notas que serão apagadas:")
			for _, note := range selected {
				fmt.Fprintf(out, "  %s\n", note)
			}
		}
		confirm, err := ask("Confirmar revisão? [s/N]: ")
		if err != nil {
			return err
		}
		if !confirmed(confirm) {
			fmt.Fprintln(out, "Revisão cancelada.")
			return nil
		}
		if err := v.Review(selected); err != nil {
			return err
		}
		fmt.Fprintln(out, "Revisão concluída.")
		return nil
	}
	if command == "destroy" {
		if err := v.ValidateDestroyCwd(cwd); err != nil {
			return err
		}
		targets, err := v.DestroyTargets()
		if err != nil {
			return err
		}
		fmt.Fprintln(out, "A destruição removerá irreversivelmente:")
		for _, target := range targets {
			fmt.Fprintf(out, "  %s\n", target)
		}
		p, err := ask("Senha: ")
		if err != nil {
			return err
		}
		phrase, err := ask("Digite a frase de confirmação: ")
		if err != nil {
			return err
		}
		expected := "DESTROY " + v.Folder
		if phrase != expected {
			return errors.New("frase de confirmação incorreta; destruição cancelada")
		}
		if err := v.Destroy(p); err != nil {
			return err
		}
		fmt.Fprintln(out, "Destruição concluída. A ação é irreversível para o Emergence; cópias externas não foram removidas.")
		return nil
	}
	if command == "status" {
		status, err := v.Status()
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "%s: %s\n", v.Folder, status)
		return nil
	}
	p, err := ask("Senha: ")
	if err != nil {
		return err
	}
	if command == "unlock" {
		if err := v.Unlock(p); err != nil {
			return err
		}
		fmt.Fprintf(out, "Pasta aberta: %s\n", v.Folder)
	} else {
		if err := v.Lock(p); err != nil {
			return err
		}
		fmt.Fprintln(out, "Pasta trancada. As cópias abertas foram removidas; não há garantia de apagamento físico ou de cópias do editor/backups.")
	}
	return nil
}

func main() {
	if err := run(os.Args[1:], os.Stdout, password); err != nil {
		fmt.Fprintln(os.Stderr, "Erro:", err)
		os.Exit(1)
	}
}
