#!/usr/bin/env bash
# Local build and install script for pc
# Builds from source and installs to /usr/local/bin
#
# Usage:
#   ./build-install.sh          # installs to /usr/local/bin (requires sudo)
#   ./build-install.sh ~/bin    # installs to custom directory

set -euo pipefail

BIN="pc"
INSTALL_DIR="${1:-/usr/local/bin}"

# Check if Go is installed
if ! command -v go &> /dev/null; then
    echo "Error: Go is not installed. Please install Go 1.21+ first." >&2
    exit 1
fi

# Determine version info from git
VERSION=$(git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo "none")
DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ)

echo "Building $BIN..."
echo "  Version: $VERSION"
echo "  Commit:  $COMMIT"
echo "  Date:    $DATE"

# Build with ldflags
LDFLAGS="-X 'main.version=$VERSION' -X 'main.commit=$COMMIT' -X 'main.date=$DATE'"
go build -ldflags "$LDFLAGS" -o "$BIN" ./cmd/pc

if [ ! -f "$BIN" ]; then
    echo "Error: Build failed, binary not found" >&2
    exit 1
fi

echo "Build successful: ./$BIN"

# Install to target directory
if [ ! -d "$INSTALL_DIR" ]; then
    echo "Error: Install directory does not exist: $INSTALL_DIR" >&2
    exit 1
fi

# Check if we need sudo
if [ -w "$INSTALL_DIR" ]; then
    echo "Installing to $INSTALL_DIR/$BIN..."
    install -m 0755 "$BIN" "$INSTALL_DIR/$BIN"
else
    echo "Installing to $INSTALL_DIR/$BIN (requires sudo)..."
    sudo install -m 0755 "$BIN" "$INSTALL_DIR/$BIN"
fi

# Clean up local binary
rm -f "$BIN"

echo ""
echo "Installation complete!"
echo "Run: $BIN --version"
"$INSTALL_DIR/$BIN" --version
