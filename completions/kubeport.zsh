#compdef kubeport

_kubeport() {
    local -a commands
    commands=(
        'start:Start the port-forward proxy in background'
        'stop:Stop the running proxy'
        'status:Show proxy status and port connectivity'
        'logs:Follow proxy logs'
        'restart:Restart the proxy'
        'fg:Run proxy in foreground'
        'foreground:Run proxy in foreground'
        'config:Configuration management'
        'version:Show version information'
        'help:Show help'
    )

    local -a config_commands
    config_commands=(
        'init:Create a new configuration file'
        'show:Display current configuration'
        'set:Set context or namespace'
        'add:Add a service to forward'
        'remove:Remove a service'
        'path:Print configuration file path'
    )

    _arguments -C \
        '(-c --config)'{-c,--config}'[Configuration file path]:file:_files' \
        '1: :->command' \
        '*: :->args'

    case $state in
        command)
            _describe -t commands 'kubeport commands' commands
            ;;
        args)
            case $words[2] in
                config)
                    if (( CURRENT == 3 )); then
                        _describe -t commands 'config subcommands' config_commands
                    else
                        case $words[3] in
                            init)
                                _arguments \
                                    '--format[Config format]:format:(yaml toml)' \
                                    '-o[Output file path]:file:_files'
                                ;;
                            set)
                                if (( CURRENT == 4 )); then
                                    _values 'key' 'context' 'namespace'
                                fi
                                ;;
                            add)
                                _arguments \
                                    '--name[Service name]:name:' \
                                    '--service[Kubernetes service name]:service:' \
                                    '--pod[Kubernetes pod name]:pod:' \
                                    '--local-port[Local port]:port:' \
                                    '--remote-port[Remote port]:port:' \
                                    '--namespace[Kubernetes namespace]:namespace:'
                                ;;
                            remove)
                                # Could complete with service names from config
                                ;;
                        esac
                    fi
                    ;;
                start|restart|fg|foreground)
                    _arguments \
                        '(-c --config)'{-c,--config}'[Configuration file]:file:_files'
                    ;;
            esac
            ;;
    esac
}

_kubeport "$@"
