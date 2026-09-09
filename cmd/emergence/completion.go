package main

import (
	"errors"
	"strings"
)

const bashCompletion = `# bash completion for emergence
_emergence_completions() {
    local cur prev command
    COMPREPLY=()
    cur="${COMP_WORDS[COMP_CWORD]}"
    prev="${COMP_WORDS[COMP_CWORD-1]}"
    command="${COMP_WORDS[1]}"
    if [[ ${COMP_CWORD} -eq 1 ]]; then
        COMPREPLY=( $(compgen -W "completion destroy doctor help inbox init lock restore review rotate-password status today trash unlock version" -- "$cur") )
        return
    fi
    case "$command" in
        completion)
            COMPREPLY=( $(compgen -W "bash zsh powershell" -- "$cur") ) ;;
        init)
            COMPREPLY=( $(compgen -W "--folder" -- "$cur") ) ;;
        doctor)
            COMPREPLY=( $(compgen -W "--check-archive --json" -- "$cur") ) ;;
        backup)
            COMPREPLY=( $(compgen -W "--output" -- "$cur") ) ;;
        restore)
            COMPREPLY=( $(compgen -W "--input" -- "$cur") ) ;;
        review)
            COMPREPLY=( $(compgen -W "--dry-run --before --after --min-size --max-size --path --json" -- "$cur") ) ;;
        status|inbox)
            COMPREPLY=( $(compgen -W "--json" -- "$cur") ) ;;
        trash)
            if [[ ${COMP_CWORD} -eq 2 ]]; then
                COMPREPLY=( $(compgen -W "list restore empty" -- "$cur") )
            fi ;;
    esac
}
complete -F _emergence_completions emergence
`

const zshCompletion = `#compdef emergence

_emergence() {
    local -a commands
    commands=(
        'completion:generate shell completion'
        'destroy:irreversibly remove managed data'
        'doctor:diagnose vault state'
        'help:show help'
        'inbox:choose the default Inbox'
        'init:initialize a vault'
        'lock:encrypt the private folder'
        'restore:restore an encrypted backup'
        'review:review Markdown notes'
        'rotate-password:change the vault password'
        'status:show vault status'
        'today:create today’s note'
        'trash:manage encrypted review quarantine'
        'unlock:open the private folder'
        'version:show version'
    )
    if (( CURRENT == 2 )); then
        _describe 'command' commands
        return
    fi
    case $words[2] in
        completion) _arguments '1:shell:(bash zsh powershell)' ;;
        init) _arguments '--folder[private folder]:folder:' ;;
        doctor) _arguments '--check-archive[authenticate sealed.age]' '--json[emit JSON]' ;;
        backup) _arguments '--output[encrypted backup path]:file:_files' ;;
        restore) _arguments '--input[encrypted backup path]:file:_files' ;;
        review) _arguments '--dry-run[show plan]' '--before[date bound]:date:' '--after[date bound]:date:' '--min-size[minimum bytes]:bytes:' '--max-size[maximum bytes]:bytes:' '--path[relative path]:path:_files -/' '--json[emit JSON]' ;;
        status|inbox) _arguments '--json[emit JSON]' ;;
        trash) _arguments '1:action:(list restore empty)' '2:entry ID:' ;;
    esac
}

if (( $+functions[compdef] )); then
    compdef _emergence emergence
fi
`

const powershellCompletion = `# PowerShell completion for emergence
Register-ArgumentCompleter -CommandName emergence -Native -ScriptBlock {
    param($wordToComplete, $commandAst, $cursorPosition)
    $commands = 'completion','destroy','doctor','help','inbox','init','lock','restore','review','rotate-password','status','today','trash','unlock','version'
    $elements = $commandAst.CommandElements
    if ($elements.Count -le 2) {
        $commands | Where-Object { $_ -like "$wordToComplete*" } | ForEach-Object {
            [System.Management.Automation.CompletionResult]::new($_, $_, 'ParameterValue', $_)
        }
        return
    }
    $command = $elements[1].Value
    $values = switch ($command) {
        'completion' { 'bash','zsh','powershell' }
        'init' { '--folder' }
        'doctor' { '--check-archive','--json' }
        'backup' { '--output' }
        'restore' { '--input' }
        'review' { '--dry-run','--before','--after','--min-size','--max-size','--path','--json' }
        'status' { '--json' }
        'inbox' { '--json' }
        'trash' { 'list','restore','empty' }
        default { @() }
    }
    $values | Where-Object { $_ -like "$wordToComplete*" } | ForEach-Object {
        [System.Management.Automation.CompletionResult]::new($_, $_, 'ParameterValue', $_)
    }
}
`

func completionScript(shell string) (string, error) {
	switch strings.ToLower(shell) {
	case "bash":
		return bashCompletion, nil
	case "zsh":
		return zshCompletion, nil
	case "powershell":
		return powershellCompletion, nil
	default:
		return "", errors.New("shell desconhecido; use bash, zsh ou powershell")
	}
}
