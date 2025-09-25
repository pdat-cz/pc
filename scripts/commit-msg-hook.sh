#!/usr/bin/env sh
# Simple Conventional Commits validator hook.
# Allows merge commits and version bumps created by release-please.
# Disallows empty subject; enforces type(scope?): message format.

set -eu

MSG_FILE="$1"
MSG=$(sed -n '1p' "$MSG_FILE")

# Allow commits that start with Merge or Revert
case "$MSG" in
  "Merge"*|"Revert"*) exit 0 ;;
esac

# Allow release-please version bump commits
case "$MSG" in
  "chore(main): release"*) exit 0 ;;
  "chore: release"*) exit 0 ;;
  "release:"*) exit 0 ;;
  "chore: update changelog"*) exit 0 ;;
  "chore: prepare release"*) exit 0 ;;
  "docs: update changelog"*) exit 0 ;;
  "ci:"*) ;; # still validate
esac

# Conventional Commits regex
# type: feat|fix|docs|style|refactor|perf|test|build|ci|chore
# optional scope in parentheses, optional !, then colon and space, then subject text
PAT='^(feat|fix|docs|style|refactor|perf|test|build|ci|chore)(\([a-z0-9_.-]+\))?!?: .+'

echo "$MSG" | grep -E -q "$PAT" && exit 0

cat >&2 <<'EOF'
Commit message does not follow Conventional Commits.
Expected format:
  type(optional-scope)!: short description
Examples:
  feat: add coils read support
  fix(tui): prevent crash when no device selected
  docs: update README install section
If this is a merge commit or created by release automation, it is allowed.
EOF
exit 1
