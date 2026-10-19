#!/usr/bin/env bash
# prepare.sh <src> <dest>: copies the variant in <src>, a folder under testdata/plain or
# testdata/faultline, to <dest>, which must not exist or be empty, and writes its go.mod: module
# example.com/leasekv, go 1.26, and for a faultline variant a requirement of faultline at
# faultline_version below. go mod tidy then fills in go.sum from the module cache (it downloads
# the module the first time). Variants keep no go.mod of their own, so no Go build in the
# repository sees them.
set -euo pipefail

faultline_version=v0.1.1

if [[ $# -ne 2 ]]; then
	echo "usage: prepare.sh <src> <dest>" >&2
	exit 2
fi
if [[ ! -d "$1" ]] || ! ls "$1"/*.go >/dev/null 2>&1; then
	echo "prepare.sh: $1 holds no Go files" >&2
	exit 2
fi
src="$(cd "$1" && pwd)"
dest="$2"
version="$(basename "$(dirname "$src")")"
if [[ "$version" != plain && "$version" != faultline ]]; then
	echo "prepare.sh: ${src} is not a variant under testdata/plain or testdata/faultline" >&2
	exit 2
fi
if [[ -e "${src}/go.mod" ]]; then
	echo "prepare.sh: ${src} has a go.mod; a variant keeps only its sources" >&2
	exit 2
fi
if [[ -e "$dest" && -n "$(ls -A "$dest")" ]]; then
	echo "prepare.sh: ${dest} is not empty" >&2
	exit 2
fi
mkdir -p "$dest"
cp -R "${src}/." "$dest/"
{
	printf 'module example.com/leasekv\n\ngo 1.26\n'
	if [[ "$version" == faultline ]]; then
		printf '\nrequire github.com/hmdsefi/faultline %s\n' "$faultline_version"
	fi
} >"${dest}/go.mod"

unset GOROOT
if [[ "$version" == faultline ]]; then
	(cd "$dest" && GOWORK=off go mod tidy)
fi
