#compdef kubeport

_kubeport() {
    local -a commands
    commands=(
        'start:Start the port-forward proxy in background'
        'stop:Stop the running proxy'
        'status:Show proxy status and port connectivity'
        'logs:Follow proxy logs'
        'restart:Restart the proxy'
        'add:Add a service to running proxy'
        'remove:Remove a service from running proxy'
        'reload:Reload config file'
        'apply:Apply services from a YAML/TOML file to running proxy'
        'mappings:Show K8s DNS to localhost address mappings'
        'watch:Watch proxy status'
        'fg:Run proxy in foreground'
        'foreground:Run proxy in foreground'
        'config:Configuration management'
        'update:Check for and apply updates'
        'version:Show version information'
        'help:Show help'
    )

    local -a config_commands
    config_commands=(
        'init:Create a new configuration file'
        'show:Display current configuration'
        'validate:Validate configuration file'
        'set:Set context or namespace'
        'add:Add a service to forward'
        'remove:Remove a service'
        'path:Print configuration file path'
    )

    local -a update_commands
    update_commands=(
        'check:Check if a newer version is available'
    )

    _arguments -C \
        '(-c --config)'{-c,--config}'[Configuration file path]:file:_files' \
        '--no-config[Ignore config file, use only CLI flags]' \
        '(--context --kube-context)'{--context,--kube-context}'[Kubernetes context]:context:' \
        '(-n --namespace)'{-n,--namespace}'[Kubernetes namespace]:namespace:' \
        '*--svc[Service spec]:spec:' \
        '*--disable-svc[Disable a service from config]:name:' \
        '--host[Connect to a remote daemon]:host\:port:' \
        '--api-key[API key for remote daemon]:key:' \
        '--time[Refresh interval for watch]:duration:' \
        '--json[Output status as JSON]' \
        '--sort[Sort status output]' \
        '--wait[Block until all forwards are connected]' \
        '--timeout[Max wait time for --wait]:duration:' \
        '(-h --help)'{-h,--help}'[Show help]' \
        '(-v --version)'{-v,--version}'[Show version]' \
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
                update)
                    if (( CURRENT == 3 )); then
                        _describe -t commands 'update subcommands' update_commands
                    fi
                    ;;
                start|restart|fg|foreground)
                    _arguments \
                        '(-c --config)'{-c,--config}'[Configuration file]:file:_files' \
                        '--wait[Block until all forwards are connected]' \
                        '--timeout[Max wait time]:duration:'
                    ;;
            esac
            ;;
    esac
}

_kubeport "$@"
