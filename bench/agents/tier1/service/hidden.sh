#!/usr/bin/env bash
# hidden.sh <plain|faultline> <dir>: runs the hidden check against the module in <dir>, a prepared
# copy (see prepare.sh). It never writes to <dir>. It builds a fresh copy from the non-test files
# of <dir>, the original *_test.go files of the version's reference, and hidden/hidden_test.go, so
# test files in <dir> cannot change the verdict. It prints the log.
#
# The module setup is pinned too, whatever <dir> holds: go.mod and go.sum are those prepare.sh
# makes for the version's reference, vendor/ and go.work are removed, and the build runs with
# GOWORK=off and GOFLAGS=-mod=mod.
#
# Exit codes: 0 the check passed, 1 a check failed (a panic or a timeout counts as failed), 2 the
# module does not compile with the hidden check, 3 or more an infrastructure error.
set -uo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if [[ $# -ne 2 || ( "$1" != plain && "$1" != faultline ) ]]; then
	echo "usage: hidden.sh <plain|faultline> <dir>" >&2
	exit 3
fi
version="$1"
dir="$2"
ref="${here}/testdata/${version}/reference"
check="${here}/testdata/${version}/hidden/hidden_test.go"
if [[ ! -f "${dir}/go.mod" ]]; then
	echo "hidden.sh: ${dir} has no go.mod" >&2
	exit 3
fi
if [[ ! -f "$check" ]] || ! ls "${ref}"/*_test.go >/dev/null 2>&1; then
	echo "hidden.sh: missing ${check} or the reference test files" >&2
	exit 3
fi
if ! command -v go >/dev/null 2>&1; then
	echo "hidden.sh: go not found" >&2
	exit 3
fi

unset GOROOT
while IFS='=' read -r name _; do
	unset "$name"
done < <(env | grep '^FAULTLINE_')
export GOMEMLIMIT="${GOMEMLIMIT:-1GiB}"
export GOWORK=off GOFLAGS=-mod=mod

work="$(mktemp -d "${TMPDIR:-/tmp}/leasekv-hidden-XXXXXX")" || exit 3
trap 'chmod -R u+w "$work" 2>/dev/null; rm -rf "$work"' EXIT
export FAULTLINE_ARTIFACTS="${work}/artifacts"

# go.mod and go.sum come from a prepared copy of the reference, never from <dir>.
if ! "${here}/prepare.sh" "$ref" "${work}/pin" >"${work}/pin.log" 2>&1; then
	cat "${work}/pin.log"
	echo "hidden.sh: could not prepare the reference go.mod" >&2
	exit 3
fi
mod="${work}/leasekv"
mkdir -p "$mod" && cp -R "${dir}/." "$mod/" || exit 3
find "$mod" -name '*_test.go' -type f -delete || exit 3
rm -rf "${mod}/vendor" "${mod}/go.work" "${mod}/go.work.sum" "${mod}/go.sum" || exit 3
cp "${work}/pin/go.mod" "${mod}/go.mod" || exit 3
if [[ -f "${work}/pin/go.sum" ]]; then
	cp "${work}/pin/go.sum" "${mod}/go.sum" || exit 3
fi
cp "${ref}"/*_test.go "$mod/" && cp "$check" "${mod}/hidden_test.go" || exit 3

build=(-vet=off)
run=(-test.run '^TestLeaseKVHidden$' -test.count=1 -test.timeout=10m)
if [[ "$version" == plain ]]; then
	build+=(-race)
	run+=(-test.parallel=60)
fi

echo "hidden.sh: ${version} check of ${dir}"
start=$(date +%s)
if ! (cd "$mod" && go test "${build[@]}" -c -o "${work}/hidden.test" . 2>&1); then
	echo "hidden.sh: build failed"
	exit 2
fi
(cd "$mod" && "${work}/hidden.test" "${run[@]}" 2>&1)
rc=$?
echo "hidden.sh: test binary exit ${rc} after $(($(date +%s) - start))s"
case $rc in
0) exit 0 ;;
1 | 2) exit 1 ;;
*) exit 3 ;;
esac
