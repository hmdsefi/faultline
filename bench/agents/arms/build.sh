#!/bin/sh
# build.sh: exports the Claude Code plugin (plugins/claude-code) at tag v0.1.1 from the repository
# that holds this script to ~/.cache/faultline-bench/plugin-v0.1.1, outside the repository, where
# the plugin arm loads it. Prints the path. Run it again to replace the export.
set -eu
tag=v0.1.1
repo=$(git -C "$(dirname "$0")" rev-parse --show-toplevel)
dest=$HOME/.cache/faultline-bench/plugin-$tag
tmp=$(mktemp -d "${TMPDIR:-/tmp}/plugin.XXXXXX")
trap 'rm -rf "$tmp"' EXIT
git -C "$repo" archive "$tag" plugins/claude-code | tar -x -C "$tmp"
test -f "$tmp/plugins/claude-code/.claude-plugin/plugin.json"
mkdir -p "$(dirname "$dest")"
rm -rf "$dest"
mv "$tmp/plugins/claude-code" "$dest"
echo "$dest"
