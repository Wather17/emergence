package vault

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Diagnostic is one deterministic, actionable result produced by Doctor.
// Paths identify the affected administrative object; note contents are never
// read into the report.
type Diagnostic struct {
	Name     string `json:"name"`
	Severity string `json:"severity"`
	Path     string `json:"path,omitempty"`
	Message  string `json:"message"`
	Action   string `json:"action,omitempty"`
}

// DoctorReport contains all checks that could be collected for a vault.
// Checks are emitted in a stable order so the report can be consumed by
// scripts without relying on translated human messages.
type DoctorReport struct {
	Root   string       `json:"root"`
	Checks []Diagnostic `json:"checks"`
}

// HasErrors reports whether the vault needs intervention before normal
// operations can safely continue.
func (r DoctorReport) HasErrors() bool {
	for _, check := range r.Checks {
		if check.Severity == "ERROR" {
			return true
		}
	}
	return false
}

func (r *DoctorReport) add(name, severity, path, message, action string) {
	r.Checks = append(r.Checks, Diagnostic{
		Name: name, Severity: severity, Path: path, Message: message, Action: action,
	})
}

func discoverRoot(start string) (string, string, error) {
	root, err := filepath.Abs(start)
	if err != nil {
		return "", "", err
	}
	info, err := plain(root)
	if err != nil {
		return "", "", fmt.Errorf("caminho inicial inválido: %w", err)
	}
	if !info.IsDir() {
		return "", "", errors.New("o caminho inicial não é uma pasta")
	}
	for {
		candidate := filepath.Join(root, metadata)
		if exists(candidate) {
			return root, candidate, nil
		}
		parent := filepath.Dir(root)
		if parent == root {
			break
		}
		root = parent
	}
	return "", "", errors.New("vault não inicializada; execute emergence init na raiz da vault")
}

func checkRegular(path string) (os.FileInfo, error) {
	info, err := plain(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("não é um arquivo regular")
	}
	return info, nil
}

func checkDirectory(path string) (os.FileInfo, error) {
	info, err := plain(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("não é uma pasta")
	}
	return info, nil
}

func appendStructuralChecks(report *DoctorReport, root, meta string) {
	if _, err := checkDirectory(meta); err != nil {
		report.add("metadata", "ERROR", meta, "a pasta .emergence está ausente ou inválida", "restaure uma cópia íntegra ou inicialize uma vault nova")
		return
	}
	report.add("metadata", "OK", meta, "a pasta .emergence é uma pasta regular", "")
	if err := validateTree(meta); err != nil {
		report.add("metadata-tree", "ERROR", meta, "a árvore de metadata contém um link ou arquivo especial", "remova o objeto inválido após preservar uma cópia da vault")
	} else {
		report.add("metadata-tree", "OK", meta, "a árvore de metadata contém apenas caminhos permitidos", "")
	}

	configPath := filepath.Join(meta, "config.json")
	var c config
	configOK := false
	if _, err := checkRegular(configPath); err != nil {
		report.add("config", "ERROR", configPath, "config.json ausente ou não é um arquivo regular", "restaure a configuração a partir de uma cópia íntegra")
	} else {
		b, err := os.ReadFile(configPath)
		if err != nil {
			report.add("config", "ERROR", configPath, "config.json não pôde ser lido", "verifique permissões e restaure uma cópia íntegra")
		} else if err := json.Unmarshal(b, &c); err != nil {
			report.add("config", "ERROR", configPath, "config.json não contém JSON válido", "restaure a configuração a partir de uma cópia íntegra")
		} else if c.Version != 1 {
			report.add("config", "ERROR", configPath, "versão de armazenamento não suportada", "use uma versão do Emergence compatível com esta vault")
		} else if err := validFolder(c.Folder); err != nil {
			report.add("config", "ERROR", configPath, "o nome da pasta privada não é portátil", "corrija a configuração somente após fazer uma cópia segura")
		} else if err := validInbox(c.Inbox); err != nil {
			report.add("config", "ERROR", configPath, "o caminho da Inbox não é válido", "corrija a configuração somente após fazer uma cópia segura")
		} else {
			configOK = true
			report.add("config", "OK", configPath, "configuração válida", "")
		}
	}

	sealedPath := filepath.Join(meta, "sealed.age")
	if _, err := checkRegular(sealedPath); err != nil {
		report.add("archive", "ERROR", sealedPath, "sealed.age ausente, ilegível ou não é um arquivo regular", "restaure o arquivo criptografado a partir de uma cópia íntegra")
	} else {
		f, err := os.Open(sealedPath)
		if err != nil {
			report.add("archive", "ERROR", sealedPath, "sealed.age não pôde ser aberto", "verifique permissões e armazenamento")
		} else if err := f.Close(); err != nil {
			report.add("archive", "ERROR", sealedPath, "sealed.age não pôde ser fechado corretamente", "verifique permissões e armazenamento")
		} else {
			report.add("archive", "OK", sealedPath, "sealed.age é legível estruturalmente", "")
		}
	}

	for _, name := range []string{"txn", "prepare", "cleanup"} {
		path := filepath.Join(meta, name)
		if !exists(path) {
			continue
		}
		if _, err := checkDirectory(path); err != nil {
			report.add(name, "ERROR", path, "estado de operação incompleto tem um tipo inválido", "preserve a vault e corrija o estado manualmente")
			continue
		}
		report.add(name, "ERROR", path, "há uma operação incompleta nesta vault", "repita o último comando antes de iniciar outra operação")
	}

	marker := filepath.Join(root, ".emergence-destroy")
	if exists(marker) {
		if _, err := checkRegular(marker); err != nil {
			report.add("destroy-marker", "ERROR", marker, "o marcador de destruição é inválido", "preserve a vault e revise a remoção interrompida")
		} else {
			report.add("destroy-marker", "ERROR", marker, "há uma destruição incompleta registrada", "repita emergence destroy somente após confirmar os alvos")
		}
	}

	if !configOK {
		return
	}
	notesPath := filepath.Join(root, c.Folder)
	if exists(notesPath) {
		if _, err := checkDirectory(notesPath); err != nil {
			report.add("private-folder", "ERROR", notesPath, "a pasta privada existe mas não é uma pasta válida", "preserve os dados e resolva o conflito de caminho")
		} else if err := validateTree(notesPath); err != nil {
			report.add("private-folder", "ERROR", notesPath, "a pasta privada contém um link ou arquivo especial", "remova o objeto inválido somente após preservar uma cópia")
		} else {
			report.add("state", "OK", notesPath, "a pasta privada está aberta", "")
		}
	} else {
		report.add("state", "OK", notesPath, "a pasta privada está trancada", "")
	}

	if c.Inbox == "" {
		report.add("inbox", "WARN", filepath.Join(root, "<não configurada>"), "nenhuma Inbox está configurada", "execute emergence inbox depois de criar ou localizar uma pasta Inbox")
	} else {
		inboxPath := filepath.Join(root, filepath.FromSlash(c.Inbox))
		if _, err := checkDirectory(inboxPath); err != nil {
			report.add("inbox", "ERROR", inboxPath, "a Inbox configurada está ausente, renomeada ou inválida", "execute emergence inbox para selecionar uma Inbox existente")
		} else {
			rel, err := filepath.Rel(notesPath, inboxPath)
			if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				report.add("inbox", "ERROR", inboxPath, "a Inbox não pode ficar dentro da pasta privada", "selecione uma Inbox fora da pasta privada")
			} else {
				report.add("inbox", "OK", inboxPath, "Inbox configurada e válida", "")
			}
		}
	}
}

// Doctor inspects a vault without opening, decrypting to disk, or changing
// any administrative file. When checkArchive is true, password is used only
// to authenticate and drain sealed.age into memory/discarded bytes.
func Doctor(start string, checkArchive bool, password string) (DoctorReport, error) {
	root, meta, err := discoverRoot(start)
	if err != nil {
		return DoctorReport{}, err
	}
	report := DoctorReport{Root: root, Checks: make([]Diagnostic, 0, 12)}
	report.add("root", "OK", root, "raiz da vault encontrada", "")
	appendStructuralChecks(&report, root, meta)
	if checkArchive {
		sealedPath := filepath.Join(meta, "sealed.age")
		if password == "" {
			report.add("archive-integrity", "ERROR", sealedPath, "a senha é necessária para verificar o arquivo criptografado", "execute em um terminal interativo e informe a senha")
		} else if _, err := checkRegular(sealedPath); err != nil {
			report.add("archive-integrity", "ERROR", sealedPath, "não foi possível verificar o arquivo criptografado", "corrija primeiro o diagnóstico estrutural")
		} else if _, err := decrypt(sealedPath, password, ""); err != nil {
			report.add("archive-integrity", "ERROR", sealedPath, "a autenticação ou estrutura do arquivo criptografado falhou", "confirme a senha e restaure uma cópia íntegra se necessário")
		} else {
			report.add("archive-integrity", "OK", sealedPath, "arquivo criptografado autenticado e validado integralmente", "")
		}
	}
	return report, nil
}
