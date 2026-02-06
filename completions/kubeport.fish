# fish completion for kubeport

# Disable file completion by default
complete -c kubeport -f

# Global options
complete -c kubeport -s c -l config -d "Configuration file path" -r

# Main commands
complete -c kubeport -n "__fish_use_subcommand" -a "start" -d "Start the port-forward proxy in background"
complete -c kubeport -n "__fish_use_subcommand" -a "stop" -d "Stop the running proxy"
complete -c kubeport -n "__fish_use_subcommand" -a "status" -d "Show proxy status and port connectivity"
complete -c kubeport -n "__fish_use_subcommand" -a "logs" -d "Follow proxy logs"
complete -c kubeport -n "__fish_use_subcommand" -a "restart" -d "Restart the proxy"
complete -c kubeport -n "__fish_use_subcommand" -a "fg" -d "Run proxy in foreground"
complete -c kubeport -n "__fish_use_subcommand" -a "foreground" -d "Run proxy in foreground"
complete -c kubeport -n "__fish_use_subcommand" -a "config" -d "Configuration management"
complete -c kubeport -n "__fish_use_subcommand" -a "version" -d "Show version information"
complete -c kubeport -n "__fish_use_subcommand" -a "help" -d "Show help"

# Config subcommands
complete -c kubeport -n "__fish_seen_subcommand_from config" -a "init" -d "Create a new configuration file"
complete -c kubeport -n "__fish_seen_subcommand_from config" -a "show" -d "Display current configuration"
complete -c kubeport -n "__fish_seen_subcommand_from config" -a "set" -d "Set context or namespace"
complete -c kubeport -n "__fish_seen_subcommand_from config" -a "add" -d "Add a service to forward"
complete -c kubeport -n "__fish_seen_subcommand_from config" -a "remove" -d "Remove a service"
complete -c kubeport -n "__fish_seen_subcommand_from config" -a "path" -d "Print configuration file path"

# Config init options
complete -c kubeport -n "__fish_seen_subcommand_from init" -l format -d "Config format" -a "yaml toml"
complete -c kubeport -n "__fish_seen_subcommand_from init" -s o -d "Output file path" -r

# Config set keys
complete -c kubeport -n "__fish_seen_subcommand_from set" -a "context" -d "Kubernetes context"
complete -c kubeport -n "__fish_seen_subcommand_from set" -a "namespace" -d "Kubernetes namespace"

# Config add options
complete -c kubeport -n "__fish_seen_subcommand_from add" -l name -d "Service name" -r
complete -c kubeport -n "__fish_seen_subcommand_from add" -l service -d "Kubernetes service name" -r
complete -c kubeport -n "__fish_seen_subcommand_from add" -l pod -d "Kubernetes pod name" -r
complete -c kubeport -n "__fish_seen_subcommand_from add" -l local-port -d "Local port" -r
complete -c kubeport -n "__fish_seen_subcommand_from add" -l remote-port -d "Remote port" -r
complete -c kubeport -n "__fish_seen_subcommand_from add" -l namespace -d "Kubernetes namespace" -r
