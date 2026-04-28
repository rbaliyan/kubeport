# fish completion for kubeport

# Disable file completion by default
complete -c kubeport -f

# Global options
complete -c kubeport -s c -l config -d "Configuration file path" -r
complete -c kubeport -l no-config -d "Ignore config file, use only CLI flags"
complete -c kubeport -l context -d "Kubernetes context" -r
complete -c kubeport -l kube-context -d "Kubernetes context" -r
complete -c kubeport -s n -l namespace -d "Kubernetes namespace" -r
complete -c kubeport -l svc -d "Service spec" -r
complete -c kubeport -l disable-svc -d "Disable a service from config" -r
complete -c kubeport -l host -d "Connect to a remote daemon" -r
complete -c kubeport -l api-key -d "API key for remote daemon" -r
complete -c kubeport -l time -d "Refresh interval for watch" -r
complete -c kubeport -l json -d "Output status as JSON"
complete -c kubeport -l sort -d "Sort status output"
complete -c kubeport -l wait -d "Block until all forwards are connected"
complete -c kubeport -l timeout -d "Max wait time for --wait" -r
complete -c kubeport -l offload -d "Add services to an already-running daemon instead of starting a new one"
complete -c kubeport -s h -l help -d "Show help"
complete -c kubeport -s v -l version -d "Show version"

# Main commands
complete -c kubeport -n "__fish_use_subcommand" -a "start" -d "Start the port-forward proxy in background"
complete -c kubeport -n "__fish_use_subcommand" -a "stop" -d "Stop the running proxy"
complete -c kubeport -n "__fish_use_subcommand" -a "status" -d "Show proxy status and port connectivity"
complete -c kubeport -n "__fish_use_subcommand" -a "logs" -d "Follow proxy logs"
complete -c kubeport -n "__fish_use_subcommand" -a "restart" -d "Restart the proxy"
complete -c kubeport -n "__fish_use_subcommand" -a "add" -d "Add a service to running proxy"
complete -c kubeport -n "__fish_use_subcommand" -a "remove" -d "Remove a service from running proxy"
complete -c kubeport -n "__fish_use_subcommand" -a "reload" -d "Reload config file"
complete -c kubeport -n "__fish_use_subcommand" -a "apply" -d "Apply services from a YAML/TOML file to running proxy"
complete -c kubeport -n "__fish_use_subcommand" -a "mappings" -d "Show K8s DNS to localhost address mappings"
complete -c kubeport -n "__fish_use_subcommand" -a "watch" -d "Watch proxy status"
complete -c kubeport -n "__fish_use_subcommand" -a "fg" -d "Run proxy in foreground"
complete -c kubeport -n "__fish_use_subcommand" -a "foreground" -d "Run proxy in foreground"
complete -c kubeport -n "__fish_use_subcommand" -a "instances" -d "List all running kubeport daemon instances"
complete -c kubeport -n "__fish_use_subcommand" -a "chaos" -d "Manage live chaos engineering settings"
complete -c kubeport -n "__fish_use_subcommand" -a "socks" -d "Start SOCKS5 proxy for Kubernetes DNS translation"
complete -c kubeport -n "__fish_use_subcommand" -a "http-proxy" -d "Start HTTP/HTTPS proxy for Kubernetes DNS translation"
complete -c kubeport -n "__fish_use_subcommand" -a "config" -d "Configuration management"
complete -c kubeport -n "__fish_use_subcommand" -a "update" -d "Check for and apply updates"
complete -c kubeport -n "__fish_use_subcommand" -a "version" -d "Show version information"
complete -c kubeport -n "__fish_use_subcommand" -a "help" -d "Show help"

# Config subcommands
complete -c kubeport -n "__fish_seen_subcommand_from config" -a "init" -d "Create a new configuration file"
complete -c kubeport -n "__fish_seen_subcommand_from config" -a "show" -d "Display current configuration"
complete -c kubeport -n "__fish_seen_subcommand_from config" -a "validate" -d "Validate configuration file"
complete -c kubeport -n "__fish_seen_subcommand_from config" -a "set" -d "Set context or namespace"
complete -c kubeport -n "__fish_seen_subcommand_from config" -a "add" -d "Add a service to forward"
complete -c kubeport -n "__fish_seen_subcommand_from config" -a "remove" -d "Remove a service"
complete -c kubeport -n "__fish_seen_subcommand_from config" -a "path" -d "Print configuration file path"

# Chaos subcommands
complete -c kubeport -n "__fish_seen_subcommand_from chaos" -a "set" -d "Apply explicit chaos params to services"
complete -c kubeport -n "__fish_seen_subcommand_from chaos" -a "enable" -d "Enable chaos for services"
complete -c kubeport -n "__fish_seen_subcommand_from chaos" -a "disable" -d "Disable chaos for services"
complete -c kubeport -n "__fish_seen_subcommand_from chaos" -a "preset" -d "Apply a named chaos preset"
complete -c kubeport -n "__fish_seen_subcommand_from chaos" -a "reset" -d "Revert to config-defined chaos settings"

# Chaos preset names
complete -c kubeport -n "__fish_seen_subcommand_from preset" -a "slow-network" -d "200ms latency spikes at 10% probability"
complete -c kubeport -n "__fish_seen_subcommand_from preset" -a "unstable-cluster" -d "5% errors + 5% 2s latency spikes"
complete -c kubeport -n "__fish_seen_subcommand_from preset" -a "packet-loss" -d "15% connection errors"

# Chaos set/enable/disable/reset flags
complete -c kubeport -n "__fish_seen_subcommand_from set enable disable reset" -l all -d "Target all services"
complete -c kubeport -n "__fish_seen_subcommand_from set" -s e -l error-rate -d "Error rate (0.0-1.0)" -r
complete -c kubeport -n "__fish_seen_subcommand_from set" -s l -l latency -d "Latency spike duration" -r
complete -c kubeport -n "__fish_seen_subcommand_from set" -s p -l spike-prob -d "Spike probability (0.0-1.0)" -r

# Update subcommands
complete -c kubeport -n "__fish_seen_subcommand_from update" -a "check" -d "Check if a newer version is available"

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
