#!/usr/bin/env bash
# Runs the tool. See docs/guide.md and docs/removed.md.
set -euo pipefail
MODE="${RUN_MODE:-normal}"
while getopts "m:" opt; do
  case $opt in
    m) MODE="$OPTARG" ;;
  esac
done
echo "$MODE" "$HOME"
