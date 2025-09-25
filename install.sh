#!/usr/bin/env bash
# Installer for pdat-cz/pc
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/pdat-cz/pc/main/install.sh | sudo bash
#   curl -fsSL https://raw.githubusercontent.com/pdat-cz/pc/main/install.sh | sudo bash -s -- v0.1.0
set -euo pipefail

REPO="pdat-cz/pc"
BIN="pc"
INSTALL_DIR="/usr/local/bin"
VERSION="${1:-latest}"

# Detect OS
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$OS" in
  linux|darwin) : ;;
  *) echo "Unsupported OS: $OS" >&2; exit 1 ;;
esac

# Detect ARCH
ARCH=$(uname -m)
case "$ARCH" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  armv7l|armv7) ARCH=armv7 ;;
  *) echo "Unsupported arch: $ARCH" >&2; exit 1 ;;
esac

ASSET="${BIN}_${OS}_${ARCH}.tar.gz"

# Resolve GitHub API URL
if [ "$VERSION" = "latest" ]; then
  API_URL="https://api.github.com/repos/${REPO}/releases/latest"
else
  API_URL="https://api.github.com/repos/${REPO}/releases/tags/${VERSION}"
fi

# Fetch release JSON once
REL_JSON=$(mktemp)
trap 'rm -f "$REL_JSON"' EXIT
curl -fsSL "$API_URL" > "$REL_JSON"

# Extract asset URL
TAR_URL=$(awk -v a="$ASSET" -F '"' '$2=="browser_download_url" && $4~a {print $4}' "$REL_JSON" | head -n1)
if [ -z "$TAR_URL" ]; then
  echo "Could not locate asset $ASSET in release $VERSION for $OS/$ARCH" >&2
  echo "Make sure a release artifact named $ASSET exists." >&2
  exit 1
fi

# Try to find a checksum source
# Prefer a consolidated SHA256SUMS file, fallback to per-asset .sha256 next to the tarball
SUMS_URL=$(awk -F '"' '$2=="browser_download_url" && $4 ~ /SHA256SUMS|checksums/i {print $4}' "$REL_JSON" | head -n1)
SHA_URL=$(awk -v a="${ASSET}.sha256" -F '"' '$2=="browser_download_url" && $4~a {print $4}' "$REL_JSON" | head -n1)

TMPDIR=$(mktemp -d)
trap 'rm -rf "$TMPDIR"' EXIT
TAR_PATH="$TMPDIR/$ASSET"

echo "Downloading: $TAR_URL"
curl -fsSL "$TAR_URL" -o "$TAR_PATH"

# Verify checksum
checksum() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  else
    echo "No sha256 tool found (sha256sum/shasum). Cannot verify checksum." >&2
    return 2
  fi
}

if [ -n "$SUMS_URL" ]; then
  echo "Verifying checksum via SHA256SUMS"
  SUMS_PATH="$TMPDIR/SHA256SUMS"
  curl -fsSL "$SUMS_URL" -o "$SUMS_PATH"
  EXPECTED=$(grep "[[:space:]]$ASSET$" "$SUMS_PATH" | awk '{print $1}') || true
  if [ -z "${EXPECTED:-}" ]; then
    echo "No checksum for $ASSET found in $(basename "$SUMS_PATH")." >&2
    exit 1
  fi
  ACTUAL=$(checksum "$TAR_PATH") || true
  if [ "${ACTUAL:-}" != "$EXPECTED" ]; then
    echo "Checksum mismatch for $ASSET" >&2
    echo "Expected: $EXPECTED" >&2
    echo "Actual:   ${ACTUAL:-<none>}" >&2
    exit 1
  fi
elif [ -n "$SHA_URL" ]; then
  echo "Verifying checksum via per-asset .sha256"
  SHA_PATH="$TMPDIR/${ASSET}.sha256"
  curl -fsSL "$SHA_URL" -o "$SHA_PATH"
  if command -v sha256sum >/dev/null 2>&1; then
    (cd "$TMPDIR" && sha256sum -c "$(basename "$SHA_PATH")")
  elif command -v shasum >/dev/null 2>&1; then
    # Normalize format and verify manually
    EXPECTED=$(awk '{print $1}' "$SHA_PATH")
    ACTUAL=$(checksum "$TAR_PATH") || true
    if [ "${ACTUAL:-}" != "$EXPECTED" ]; then
      echo "Checksum mismatch for $ASSET" >&2
      echo "Expected: $EXPECTED" >&2
      echo "Actual:   ${ACTUAL:-<none>}" >&2
      exit 1
    fi
  else
    echo "No sha256 tool found (sha256sum/shasum). Cannot verify checksum." >&2
    exit 2
  fi
else
  echo "Warning: No checksum file found in the release. Skipping verification." >&2
fi

# Extract and install
UNPACK="$TMPDIR/unpack"
mkdir -p "$UNPACK"
 tar -xzf "$TAR_PATH" -C "$UNPACK"
if [ ! -x "$UNPACK/$BIN" ]; then
  echo "Expected binary $BIN not found in the archive." >&2
  exit 1
fi

# Install requires root if INSTALL_DIR is root-owned
install -m 0755 "$UNPACK/$BIN" "$INSTALL_DIR/$BIN"

echo "Installed $BIN to $INSTALL_DIR/$BIN"
"$INSTALL_DIR/$BIN" -h || true
