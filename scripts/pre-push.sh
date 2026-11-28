#!/usr/bin/env bash
set -e

# hermEX Fast Pre-push Hook (<9s budget)
# Runs format check, lint, and typecheck only on changed files before pushing.

remote="$1"
url="$2"
z40="0000000000000000000000000000000000000000"

changed_files=""

if [ -t 0 ]; then
  # Running interactively or directly
  changed_files=$(git diff --name-only origin/main...HEAD 2>/dev/null || git diff --name-only HEAD~1 2>/dev/null || true)
else
  while read -r local_ref local_sha remote_ref remote_sha; do
    if [ "$local_sha" = "$z40" ]; then
      # Deleting branch, nothing to check
      exit 0
    fi
    if [ "$remote_sha" = "$z40" ]; then
      range="origin/main...$local_sha"
    else
      range="$remote_sha...$local_sha"
    fi
    files=$(git diff --name-only "$range" 2>/dev/null || git diff --name-only HEAD~1 2>/dev/null || true)
    changed_files=$(printf "%s\n%s" "$changed_files" "$files")
  done
fi

changed_files=$(echo "$changed_files" | grep -v '^$' | sort -u || true)

if [ -z "$changed_files" ]; then
  exit 0
fi

start_time=$(date +%s)
echo "==> hermEX pre-push gate running on changed files..."

# 1. Format check on changed Go files
go_files=$(echo "$changed_files" | grep -E '\.go$' || true)
if [ -n "$go_files" ]; then
  fmt_out=$(gofmt -l $go_files 2>/dev/null || true)
  if [ -n "$fmt_out" ]; then
    echo "ERROR: The following Go files require gofmt:"
    echo "$fmt_out"
    exit 1
  fi
  echo "✓ Format check passed"
fi

# 2. Go vet on touched Go packages
if [ -n "$go_files" ]; then
  go_dirs=$(for f in $go_files; do dirname "$f"; done | sort -u)
  for dir in $go_dirs; do
    go vet "./$dir"
  done
  echo "✓ Go vet passed"
fi

# 3. Webmail2 SPA checks if any webmail2 source changed
spa_files=$(echo "$changed_files" | grep '^internal/webmail2/src/' || true)
if [ -n "$spa_files" ]; then
  echo "==> Running SPA fast lint & typecheck..."
  (
    cd internal/webmail2
    npm run lint
    npm run typecheck
  )
  echo "✓ SPA lint & typecheck passed"
fi

end_time=$(date +%s)
duration=$((end_time - start_time))
echo "==> Pre-push verification passed in ${duration}s (budget: <9s)"
exit 0
