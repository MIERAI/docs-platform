#!/usr/bin/env bash
# Runs every fuzz target listed in the given file ("<package> <FuzzFunc>" per line) for
# FUZZTIME each. A crash leaves its input under the package's testdata/fuzz/ for a regression test.
set -euo pipefail
GO="${GO:-go}"
FUZZTIME="${FUZZTIME:-30s}"
list="${1:?usage: fuzz.sh <targets file>}"
while read -r pkg fn; do
  [[ -z "$pkg" || "$pkg" == \#* ]] && continue
  echo "fuzz $pkg $fn ($FUZZTIME)"
  "$GO" test "$pkg" -run='^$' -fuzz="^${fn}\$" -fuzztime="$FUZZTIME"
done < "$list"
