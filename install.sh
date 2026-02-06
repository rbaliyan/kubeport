#!/bin/sh
# install.sh - Install kubeport CLI
#
# Usage:
#   curl -sSfL https://raw.githubusercontent.com/rbaliyan/kubeport/main/install.sh | sh
#   curl -sSfL https://raw.githubusercontent.com/rbaliyan/kubeport/main/install.sh | sh -s -- -b /usr/local/bin
#   curl -sSfL https://raw.githubusercontent.com/rbaliyan/kubeport/main/install.sh | sh -s -- -b ~/.local/bin v1.0.0

set -e

REPO="rbaliyan/kubeport"
BINARY="kubeport"
INSTALL_DIR="${HOME}/.local/bin"
VERSION=""

usage() {
    cat <<EOF
Install kubeport CLI

Usage:
  install.sh [options] [version]

Options:
  -b DIR    Install to DIR (default: ~/.local/bin)
  -h        Show this help

Examples:
  install.sh                    # Install latest to ~/.local/bin
  install.sh -b /usr/local/bin  # Install latest to /usr/local/bin
  install.sh v1.0.0             # Install specific version
EOF
}

# Parse arguments
while [ $# -gt 0 ]; do
    case "$1" in
        -b)
            INSTALL_DIR="$2"
            shift 2
            ;;
        -h|--help)
            usage
            exit 0
            ;;
        v*)
            VERSION="$1"
            shift
            ;;
        *)
            echo "Unknown option: $1"
            usage
            exit 1
            ;;
    esac
done

# Detect OS and architecture
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)

case "$ARCH" in
    x86_64|amd64)
        ARCH="amd64"
        ;;
    aarch64|arm64)
        ARCH="arm64"
        ;;
    *)
        echo "Unsupported architecture: $ARCH"
        exit 1
        ;;
esac

case "$OS" in
    darwin|linux)
        ;;
    mingw*|msys*|cygwin*)
        OS="windows"
        ;;
    *)
        echo "Unsupported OS: $OS"
        exit 1
        ;;
esac

# Get latest version if not specified
if [ -z "$VERSION" ]; then
    VERSION=$(curl -sSfL "https://api.github.com/repos/${REPO}/releases/latest" | grep '"tag_name"' | sed -E 's/.*"([^"]+)".*/\1/')
    if [ -z "$VERSION" ]; then
        echo "Failed to get latest version"
        exit 1
    fi
fi

# Remove v prefix for filename
VERSION_NUM="${VERSION#v}"

# Build download URL
if [ "$OS" = "windows" ]; then
    FILENAME="${BINARY}_${VERSION_NUM}_${OS}_${ARCH}.zip"
else
    FILENAME="${BINARY}_${VERSION_NUM}_${OS}_${ARCH}.tar.gz"
fi
URL="https://github.com/${REPO}/releases/download/${VERSION}/${FILENAME}"

echo "Installing ${BINARY} ${VERSION}..."
echo "  OS:      ${OS}"
echo "  Arch:    ${ARCH}"
echo "  Install: ${INSTALL_DIR}"

# Create temp directory
TMP_DIR=$(mktemp -d)
trap "rm -rf ${TMP_DIR}" EXIT

# Download
echo "Downloading ${URL}..."
curl -sSfL -o "${TMP_DIR}/${FILENAME}" "${URL}"

# Extract
cd "${TMP_DIR}"
if [ "$OS" = "windows" ]; then
    unzip -q "${FILENAME}"
else
    tar -xzf "${FILENAME}"
fi

# Install
mkdir -p "${INSTALL_DIR}"
if [ "$OS" = "windows" ]; then
    cp "${BINARY}.exe" "${INSTALL_DIR}/"
else
    cp "${BINARY}" "${INSTALL_DIR}/"
    chmod +x "${INSTALL_DIR}/${BINARY}"
fi

echo ""
echo "Installed ${BINARY} to ${INSTALL_DIR}/${BINARY}"
echo ""

# Check if in PATH
if ! echo "$PATH" | grep -q "${INSTALL_DIR}"; then
    echo "NOTE: ${INSTALL_DIR} is not in your PATH."
    echo "Add this to your shell profile:"
    echo ""
    echo "  export PATH=\"\$PATH:${INSTALL_DIR}\""
    echo ""
fi

echo "Run '${BINARY} version' to verify installation."
