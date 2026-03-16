#!/usr/bin/env bash
#
# Record a kubeport demo and generate an animated GIF.
#
# This script records a terminal session using asciinema, then converts it
# to a GIF using agg (asciinema gif generator). The recording showcases
# kubeport's core workflow: config file, starting the daemon, checking
# status, dynamic service management, and graceful shutdown.
#
# The script creates a clean temporary directory at /tmp/kubeport-demo,
# copies the binary and config there, and runs everything from that path
# so no local filesystem paths leak into the recording.
#
# Dependencies:
#   asciinema - terminal session recorder
#     brew install asciinema
#
#   agg - converts asciinema .cast files to animated GIFs
#     brew install agg
#
# Prerequisites:
#   1. A kind cluster with demo services deployed:
#        kubectl apply -f demo/k8s-setup.yaml
#
#   2. kubeport binary built:
#        just build
#
# Usage (from repo root):
#   ./demo/demo.sh [--output <path>]
#
# Options:
#   --output, -o <path>   Output path for the GIF (file or directory)
#                          Default: demo/demo.gif
#
# Output:
#   demo/demo.gif (or the path specified by --output)

set -euo pipefail

if [[ "${1:-}" == "--help" || "${1:-}" == "-h" ]]; then
    sed -n '2,/^[^#]/{ /^#/s/^# \{0,1\}//p }' "$0"
    exit 0
fi

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
DEMO_DIR="/tmp/kubeport-demo"
CAST_FILE="$DEMO_DIR/demo.cast"
GIF_OUTPUT="$SCRIPT_DIR/demo.gif"

# Parse arguments
while [[ $# -gt 0 ]]; do
    case "$1" in
        --output|-o)
            if [[ -z "${2:-}" ]]; then
                echo "Error: --output requires a path"
                exit 1
            fi
            output="$2"
            if [[ -d "$output" ]]; then
                GIF_OUTPUT="$(cd "$output" && pwd)/demo.gif"
            else
                parent="$(cd "$(dirname "$output")" 2>/dev/null && pwd)" || {
                    echo "Error: parent directory of $output does not exist"
                    exit 1
                }
                GIF_OUTPUT="$parent/$(basename "$output")"
            fi
            shift 2
            ;;
        *)
            echo "Unknown option: $1"
            echo "Run with --help for usage"
            exit 1
            ;;
    esac
done

# Check dependencies
for cmd in asciinema agg; do
    if ! command -v "$cmd" &>/dev/null; then
        echo "Error: $cmd is not installed. Run: brew install $cmd"
        exit 1
    fi
done

if [[ ! -f "$REPO_DIR/bin/kubeport" ]]; then
    echo "Error: bin/kubeport not found. Run: just build"
    exit 1
fi

# Set up clean demo directory with binary and config
if [[ "$DEMO_DIR" == /tmp/* ]]; then rm -rf "$DEMO_DIR"; fi
mkdir -p "$DEMO_DIR/bin"
cp "$REPO_DIR/bin/kubeport" "$DEMO_DIR/bin/"
cp "$SCRIPT_DIR/kubeport.yaml" "$DEMO_DIR/"

# --- Inner script that asciinema records ---
record_demo() {
    export PATH="$DEMO_DIR/bin:$PATH"
    cd "$DEMO_DIR"

    # Ensure clean state
    kubeport stop 2>/dev/null || true

    slow_type() {
        local text="$1"
        for ((i = 0; i < ${#text}; i++)); do
            printf "%s" "${text:$i:1}"
            sleep 0.04
        done
    }

    run_cmd() {
        local cmd="$1"
        local post_delay="${2:-2}"
        printf "\n\033[1;32m\$\033[0m "
        slow_type "$cmd"
        sleep 0.3
        printf "\n"
        eval "$cmd"
        sleep "$post_delay"
    }

    comment() {
        printf "\n\033[1;36m%s\033[0m\n" "$1"
        sleep 1
    }

    reset
    comment "# kubeport - persistent Kubernetes port-forward supervisor"

    comment "# Here's our config file:"
    run_cmd "cat kubeport.yaml" 3

    comment "# Start the daemon"
    run_cmd "kubeport start" 4

    comment "# Dynamic ports assigned — Cache and Platform ports chosen by the OS"
    run_cmd "kubeport status" 4

    comment "# The web API is forwarded - let's verify"
    run_cmd "curl -s localhost:8080 | head -4" 2

    comment "# Generate some traffic..."
    run_cmd "for i in \$(seq 1 10); do curl -s localhost:8080 > /dev/null; done" 3

    comment "# Status now shows bytes transferred and throughput"
    run_cmd "kubeport status" 4

    comment "# Reach services by Kubernetes FQDN via the SOCKS5 proxy"
    run_cmd "kubeport socks &>/dev/null &" 2
    run_cmd "curl -s --proxy socks5h://localhost:1080 http://web-api.demo.svc.cluster.local | head -4" 3
    run_cmd "kill %1 2>/dev/null; wait %1 2>/dev/null; true" 1

    comment "# All address mappings kubeport knows about"
    run_cmd "kubeport mappings" 3

    comment "# Add a service dynamically (local port assigned automatically)"
    run_cmd "kubeport add --name \"Extra\" --service web-api --remote-port 80 -n demo" 3

    run_cmd "kubeport status" 3

    comment "# Remove it just as easily"
    run_cmd "kubeport remove \"Extra\"" 2

    comment "# Graceful shutdown"
    run_cmd "kubeport stop" 2

    printf "\n\033[1;32mAll connections closed. That's kubeport!\033[0m\n"
    sleep 3
}

# Export the function so asciinema's subshell can use it
export DEMO_DIR
export -f record_demo

echo "Recording demo..."
asciinema rec --overwrite -c 'bash -c "$(declare -f record_demo); record_demo"' "$CAST_FILE"

echo ""
echo "Generating GIF..."
agg --font-size 16 --cols 100 --rows 30 "$CAST_FILE" "$GIF_OUTPUT"

# Clean up temp directory
if [[ "$DEMO_DIR" == /tmp/* ]]; then rm -rf "$DEMO_DIR"; fi

echo ""
echo "Done! GIF saved to: $GIF_OUTPUT"
