#!/bin/bash
# bash completion for kubeport

_kubeport() {
    local cur prev commands config_commands
    COMPREPLY=()
    cur="${COMP_WORDS[COMP_CWORD]}"
    prev="${COMP_WORDS[COMP_CWORD-1]}"

    commands="start stop status logs restart fg foreground config version help"
    config_commands="init show set add remove path"

    case "${prev}" in
        kubeport)
            COMPREPLY=( $(compgen -W "${commands}" -- "${cur}") )
            return 0
            ;;
        config)
            COMPREPLY=( $(compgen -W "${config_commands}" -- "${cur}") )
            return 0
            ;;
        -c|--config)
            COMPREPLY=( $(compgen -f -- "${cur}") )
            return 0
            ;;
        init)
            COMPREPLY=( $(compgen -W "--format -o" -- "${cur}") )
            return 0
            ;;
        --format)
            COMPREPLY=( $(compgen -W "yaml toml" -- "${cur}") )
            return 0
            ;;
        set)
            COMPREPLY=( $(compgen -W "context namespace" -- "${cur}") )
            return 0
            ;;
        add)
            COMPREPLY=( $(compgen -W "--name --service --pod --local-port --remote-port --namespace" -- "${cur}") )
            return 0
            ;;
        *)
            ;;
    esac

    # Global flags available for most commands
    if [[ "${cur}" == -* ]]; then
        COMPREPLY=( $(compgen -W "-c --config -h --help" -- "${cur}") )
        return 0
    fi

    COMPREPLY=( $(compgen -W "${commands}" -- "${cur}") )
}

complete -F _kubeport kubeport
