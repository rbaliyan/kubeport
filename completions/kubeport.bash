#!/bin/bash
# bash completion for kubeport

_kubeport() {
    local cur prev commands config_commands chaos_commands
    COMPREPLY=()
    cur="${COMP_WORDS[COMP_CWORD]}"
    prev="${COMP_WORDS[COMP_CWORD-1]}"

    commands="start stop status logs restart add remove reload apply mappings watch fg foreground instances socks http-proxy chaos config update version help"
    config_commands="init show validate set add remove path"
    update_commands="check"
    chaos_commands="set enable disable preset reset help"

    case "${prev}" in
        kubeport)
            COMPREPLY=( $(compgen -W "${commands}" -- "${cur}") )
            return 0
            ;;
        config)
            COMPREPLY=( $(compgen -W "${config_commands}" -- "${cur}") )
            return 0
            ;;
        update)
            COMPREPLY=( $(compgen -W "${update_commands}" -- "${cur}") )
            return 0
            ;;
        chaos)
            COMPREPLY=( $(compgen -W "${chaos_commands}" -- "${cur}") )
            return 0
            ;;
        preset)
            COMPREPLY=( $(compgen -W "slow-network unstable-cluster packet-loss" -- "${cur}") )
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
        *)
            ;;
    esac

    # Global flags available for most commands
    if [[ "${cur}" == -* ]]; then
        COMPREPLY=( $(compgen -W "-c --config --no-config --context --kube-context -n --namespace --svc --disable-svc --host --api-key --json --sort --wait --timeout --time --offload -h --help -v --version" -- "${cur}") )
        return 0
    fi

    COMPREPLY=( $(compgen -W "${commands}" -- "${cur}") )
}

complete -F _kubeport kubeport
