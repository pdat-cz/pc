#!/usr/bin/env sh
set -euo pipefail

# Installs Conventional Commits validator as .git/hooks/commit-msg
# Usage: scripts/setup-git-hooks.sh

REPO_ROOT=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
HOOKS_DIR="$REPO_ROOT/.git/hooks"
TEMPLATE="$REPO_ROOT/scripts/commit-msg-hook.sh"

if [ ! -d "$REPO_ROOT/.git" ]; then
  echo "This does not look like a git working tree: $REPO_ROOT" >&2
  exit 1
fi

mkdir -p "$HOOKS_DIR"
install -m 0755 "$TEMPLATE" "$HOOKS_DIR/commit-msg"
echo "Installed commit-msg hook to $HOOKS_DIR/commit-msg"
