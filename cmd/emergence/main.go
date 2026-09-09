package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

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
  emergence status
  emergence version
  emergence help

Execute init na raiz da vault. Os demais comandos também funcionam em subpastas.
Encerre a edição antes de lock. A senha é solicitada no terminal, sem exibição.
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
	if command != "init" && command != "unlock" && command != "lock" && command != "status" {
		return fmt.Errorf("comando desconhecido: %s; use emergence help", command)
	}
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(out)
	folder := "Morning Pages"
	if command == "init" {
		fs.StringVar(&folder, "folder", folder, "nome da pasta privada")
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
		return nil
	}
	v, err := vault.Open(cwd)
	if err != nil {
		return err
	}
	defer v.Close()
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
