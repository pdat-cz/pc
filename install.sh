#!/usr/bin/env bash
# Installer for pdat-cz/pc
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/pdat-cz/pc/main/install.sh | sudo bash
#   curl -fsSL https://raw.githubusercontent.com/pdat-cz/pc/main/install.sh | sudo bash -s -- v0.1.8
set -euo pipefail

REPO="pdat-cz/pc"
BIN="pc"
INSTALL_DIR="/usr/local/bin"
VERSION="${1:-latest}"   # accepts "latest" or a tag like v0.1.8 or 0.1.8

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

# Resolve GitHub API URL
if [ "$VERSION" = "latest" ]; then
  API_URL="https://api.github.com/repos/${REPO}/releases/latest"
else
  # Accept both 0.1.8 and v0.1.8 as input, but the API wants the tag as published (usually vX.Y.Z)
  TAG="$VERSION"
  case "$TAG" in
    v*) : ;;
    *) TAG="v$TAG" ;;
  esac
  API_URL="https://api.github.com/repos/${REPO}/releases/tags/${TAG}"
fi

# Fetch release JSON once
REL_JSON=$(mktemp)
trap 'rm -f "$REL_JSON"' EXIT
# Tip: set GITHUB_TOKEN to avoid rate limiting and add: -H "Authorization: Bearer $GITHUB_TOKEN"
curl -fsSL "$API_URL" > "$REL_JSON"

# Determine the version string used in asset names (no leading v)
TAG_NAME=$(awk -F '"' '/"tag_name"/ {print $4; exit}' "$REL_JSON")
if [ -z "${TAG_NAME:-}" ]; then
  echo "Could not determine tag_name from release metadata" >&2
  exit 1
fi
VER=${TAG_NAME#v}   # strip leading v if present

ASSET="${BIN}_${VER}_${OS}_${ARCH}.tar.gz"

# Extract asset URL
TAR_URL=$(awk -v a="$ASSET" -F '"' '$2=="browser_download_url" && $4~a {print $4}' "$REL_JSON" | head -n1)
if [ -z "$TAR_URL" ]; then
  echo "Could not locate asset $ASSET in release $TAG_NAME for $OS/$ARCH" >&2
  echo "Make sure a release artifact named $ASSET exists." >&2
  exit 1
fi

# Try to find a checksum source
# Prefer a consolidated SHA256SUMS/checksums file, fallback to per-asset .sha256 next to the tarball
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
  echo "Verifying checksum via checksums file"
  SUMS_PATH="$TMPDIR/checksums.txt"
  curl -fsSL "$SUMS_URL" -o "$SUMS_PATH"
  # GoReleaser checksums.txt default format: "<sha256>  <filename>"
  EXPECTED=$(grep -E "[[:space:]]$ASSET$" "$SUMS_PATH" | awk '{print $1}') || true
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
