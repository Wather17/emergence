package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

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
  emergence today
  emergence backup --output <arquivo>
  emergence restore --input <arquivo>
  emergence status
  emergence doctor [--check-archive]
  emergence inbox
  emergence review [--dry-run]
  emergence destroy
  emergence version
  emergence help

Execute init na raiz da vault. Os demais comandos também funcionam em subpastas.
Encerre a edição antes de lock. A senha é solicitada no terminal, sem exibição.
destroy exige um terminal interativo, valida a senha e a frase literal
DESTROY <nome-da-pasta-privada> antes de remover a pasta privada e .emergence.
`

type statusJSON struct {
	SchemaVersion int    `json:"schema_version"`
	Command       string `json:"command"`
	Root          string `json:"root"`
	Folder        string `json:"folder"`
	Inbox         string `json:"inbox"`
	State         string `json:"state"`
}

type inboxJSON struct {
	SchemaVersion int      `json:"schema_version"`
	Command       string   `json:"command"`
	Candidates    []string `json:"candidates"`
	Selected      string   `json:"selected"`
}

type doctorJSON struct {
	SchemaVersion int                `json:"schema_version"`
	Command       string             `json:"command"`
	Root          string             `json:"root"`
	OK            bool               `json:"ok"`
	Checks        []vault.Diagnostic `json:"checks"`
}

type reviewJSON struct {
	SchemaVersion int                     `json:"schema_version"`
	Command       string                  `json:"command"`
	Entries       []vault.ReviewPlanEntry `json:"entries"`
}

func writeDocument(out io.Writer, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = out.Write(b)
	return err
}

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

func parseReviewFilter(before, after string, minSize, maxSize int64, minSet, maxSet bool, pathPrefix string) (vault.ReviewFilter, error) {
	parseDate := func(raw string) (*time.Time, error) {
		if raw == "" {
			return nil, nil
		}
		parsed, err := time.Parse("2006-01-02", raw)
		if err != nil {
			return nil, fmt.Errorf("data inválida %q; use YYYY-MM-DD", raw)
		}
		local := time.Date(parsed.Year(), parsed.Month(), parsed.Day(), 0, 0, 0, 0, time.Local)
		return &local, nil
	}
	beforeTime, err := parseDate(before)
	if err != nil {
		return vault.ReviewFilter{}, err
	}
	afterTime, err := parseDate(after)
	if err != nil {
		return vault.ReviewFilter{}, err
	}
	filter := vault.ReviewFilter{Before: beforeTime, After: afterTime, PathPrefix: pathPrefix}
	if minSet {
		if minSize < 0 {
			return vault.ReviewFilter{}, errors.New("o tamanho deve ser um inteiro não negativo")
		}
		filter.MinSize = &minSize
	}
	if maxSet {
		if maxSize < 0 {
			return vault.ReviewFilter{}, errors.New("o tamanho deve ser um inteiro não negativo")
		}
		filter.MaxSize = &maxSize
	}
	if err := filter.Validate(); err != nil {
		return vault.ReviewFilter{}, err
	}
	return filter, nil
}

func hasFlag(args []string, name string) bool {
	for _, arg := range args {
		if arg == name || strings.HasPrefix(arg, name+"=") {
			return true
		}
	}
	return false
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
	if command != "init" && command != "unlock" && command != "lock" && command != "rotate-password" && command != "today" && command != "backup" && command != "restore" && command != "status" && command != "doctor" && command != "inbox" && command != "review" && command != "destroy" {
		return fmt.Errorf("comando desconhecido: %s; use emergence help", command)
	}
	jsonOutput := hasFlag(args[1:], "--json")
	if jsonOutput && command != "status" && command != "doctor" && command != "inbox" && command != "review" {
		return errors.New("--json só está disponível para status, doctor, inbox e review --dry-run")
	}
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	if jsonOutput {
		fs.SetOutput(io.Discard)
	} else {
		fs.SetOutput(out)
	}
	folder := "Morning Pages"
	if command == "init" {
		fs.StringVar(&folder, "folder", folder, "nome da pasta privada")
	}
	checkArchive := false
	dryRun := false
	output := ""
	input := ""
	before := ""
	after := ""
	minSize := int64(-1)
	maxSize := int64(-1)
	pathPrefix := ""
	if command == "doctor" {
		fs.BoolVar(&checkArchive, "check-archive", false, "autenticar e validar integralmente sealed.age")
	}
	if command == "backup" {
		fs.StringVar(&output, "output", "", "arquivo de saída do backup criptografado")
	}
	if command == "restore" {
		fs.StringVar(&input, "input", "", "arquivo de backup criptografado")
	}
	if command == "review" {
		fs.BoolVar(&dryRun, "dry-run", false, "mostrar o plano sem alterar arquivos")
		fs.StringVar(&before, "before", "", "incluir notas modificadas antes de YYYY-MM-DD")
		fs.StringVar(&after, "after", "", "incluir notas modificadas em/depois de YYYY-MM-DD")
		fs.Int64Var(&minSize, "min-size", -1, "tamanho mínimo em bytes")
		fs.Int64Var(&maxSize, "max-size", -1, "tamanho máximo em bytes")
		fs.StringVar(&pathPrefix, "path", "", "prefixo de subpasta relativo")
	}
	if command == "status" || command == "doctor" || command == "inbox" {
		fs.BoolVar(&jsonOutput, "json", false, "emitir saída JSON versão 1")
	}
	if command == "review" {
		fs.BoolVar(&jsonOutput, "json", false, "emitir saída JSON versão 1")
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
	reviewFilter := vault.ReviewFilter{}
	if command == "review" {
		if jsonOutput && !dryRun {
			return errors.New("review --json exige --dry-run")
		}
		var filterErr error
		reviewFilter, filterErr = parseReviewFilter(before, after, minSize, maxSize, hasFlag(args[1:], "--min-size"), hasFlag(args[1:], "--max-size"), pathPrefix)
		if filterErr != nil {
			return filterErr
		}
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
		if jsonOutput {
			err := writeDocument(out, doctorJSON{SchemaVersion: 1, Command: "doctor", Root: report.Root, OK: !report.HasErrors(), Checks: report.Checks})
			if err != nil {
				return err
			}
			if report.HasErrors() {
				return errors.New("doctor encontrou problemas que exigem intervenção")
			}
			return nil
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
	if command == "restore" {
		if input == "" {
			return errors.New("informe um arquivo de entrada com --input")
		}
		p, err := ask("Senha do backup: ")
		if err != nil {
			return err
		}
		if err := vault.Restore(cwd, input, p); err != nil {
			return err
		}
		fmt.Fprintln(out, "Vault restaurada e trancada.")
		return nil
	}
	v, err := vault.Open(cwd)
	if err != nil {
		return err
	}
	defer v.Close()
	if command == "today" {
		name, err := v.Today()
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "Nota diária criada: %s\n", name)
		return nil
	}
	if command == "backup" {
		if output == "" {
			return errors.New("informe um arquivo de saída com --output")
		}
		p, err := ask("Senha: ")
		if err != nil {
			return err
		}
		if err := v.Backup(output, p); err != nil {
			return err
		}
		fmt.Fprintf(out, "Backup criptografado criado: %s\n", output)
		return nil
	}
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
		if jsonOutput {
			if candidates == nil {
				candidates = []string{}
			}
			return writeDocument(out, inboxJSON{SchemaVersion: 1, Command: "inbox", Candidates: candidates, Selected: v.InboxPath()})
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
			if dryRun {
				return errors.New("há uma revisão incompleta; dry-run não pode retomar uma transação")
			}
			fmt.Fprintln(out, "Retomando revisão incompleta...")
			if err := v.Review(nil); err != nil {
				return err
			}
			fmt.Fprintln(out, "Revisão concluída.")
			return nil
		}
		noteInfo, err := v.MarkdownNoteInfo(reviewFilter)
		if err != nil {
			return err
		}
		notes := make([]string, 0, len(noteInfo))
		for _, note := range noteInfo {
			notes = append(notes, note.Name)
		}
		if len(notes) == 0 {
			if jsonOutput {
				return writeDocument(out, reviewJSON{SchemaVersion: 1, Command: "review", Entries: []vault.ReviewPlanEntry{}})
			}
			fmt.Fprintln(out, "Nenhuma nota Markdown na pasta privada.")
			return nil
		}
		reviewOut := out
		if jsonOutput {
			reviewOut = os.Stderr
		}
		fmt.Fprintln(reviewOut, "Notas Markdown:")
		for i, note := range notes {
			fmt.Fprintf(reviewOut, "  %d) %s (%d bytes, modificação %s)\n", i+1, note, noteInfo[i].Size, noteInfo[i].ModTime.Format("2006-01-02 15:04:05 -07:00"))
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
		if dryRun {
			plan, err := v.ReviewPlanWithFilter(selected, reviewFilter)
			if err != nil {
				return err
			}
			if jsonOutput {
				return writeDocument(out, reviewJSON{SchemaVersion: 1, Command: "review", Entries: plan})
			}
			fmt.Fprintln(out, "Plano da revisão (dry-run):")
			for _, item := range plan {
				if item.Action == "delete" {
					fmt.Fprintf(out, "  apagar %s (%d bytes, sha256 %s)\n", item.Name, item.Size, item.Hash)
				} else {
					fmt.Fprintf(out, "  mover %s -> %s (%d bytes, sha256 %s)\n", item.Name, item.Destination, item.Size, item.Hash)
				}
			}
			fmt.Fprintln(out, "Nenhum arquivo foi alterado.")
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
		if err := v.ReviewWithFilter(selected, reviewFilter); err != nil {
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
		if jsonOutput {
			state := status
			if strings.HasPrefix(state, "incompleta") {
				state = "incompleta"
			}
			return writeDocument(out, statusJSON{SchemaVersion: 1, Command: "status", Root: v.Root, Folder: v.Folder, Inbox: v.InboxPath(), State: state})
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
