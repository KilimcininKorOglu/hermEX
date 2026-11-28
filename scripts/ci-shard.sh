#!/bin/sh
shard="$1"
all=$(go list ./internal/... ./cmd/...)
case "$shard" in
  1)
    echo "$all" | grep -E '^hermex/internal/(ews|objectstore)($|/)' | tr '\n' ' '
    ;;
  2)
    echo "$all" | grep -E '^hermex/internal/(directory|admin|migrate|logging|activesync)($|/)' | tr '\n' ' '
    ;;
  3)
    echo "$all" | grep -vE '^hermex/internal/(ews|objectstore|directory|admin|migrate|logging|activesync)($|/)' | tr '\n' ' '
    ;;
  *)
    echo "Unknown shard: $shard (expected 1, 2, or 3)" >&2
    exit 1
    ;;
esac
echo
