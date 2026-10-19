#!/usr/bin/env bash
# logs.sh: writes logs/<version>-<bug>.txt, the CI log of each candidate: the output of a real
# failing `go test ./...` in a prepared copy of the variant. faultline: the visible test, whose
# artifact directory is rewritten to /tmp/faultline-1001/ (where faultline writes it on a Linux
# runner). plain: the first of up to 60 runs that fails on the same check as the faultline log,
# so both versions show the same symptom. faultline comes from the module cache at the version
# prepare.sh requires.
set -uo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
unset GOROOT
while IFS='=' read -r name _; do
	unset "$name"
done < <(env | grep '^FAULTLINE_')
export GOMEMLIMIT="${GOMEMLIMIT:-1GiB}"
work="$(mktemp -d "${TMPDIR:-/tmp}/leasekv-logs-XXXXXX")" || exit 2
trap 'chmod -R u+w "$work" 2>/dev/null; rm -rf "$work"' EXIT

for bug in trunc sync fwd epoch; do
	d="${work}/faultline-${bug}"
	"${here}/prepare.sh" "${here}/testdata/faultline/${bug}" "$d" >/dev/null || exit 2
	(cd "$d" && FAULTLINE_ARTIFACTS="${work}/art" go test ./... >"${work}/out" 2>&1)
	sed -E "s|artifacts: .*/art/|artifacts: /tmp/faultline-1001/|" "${work}/out" >"${here}/logs/faultline-${bug}.txt"
	check="$(grep -m1 -oE '(invariant|final check) "[^"]+" (violated|failed)' "${work}/out" | cut -d'"' -f2)"
	echo "faultline-${bug}: ${check}"

	d="${work}/plain-${bug}"
	"${here}/prepare.sh" "${here}/testdata/plain/${bug}" "$d" >/dev/null || exit 2
	found=""
	for i in $(seq 1 60); do
		(cd "$d" && go test -count=1 ./... >"${work}/out" 2>&1)
		if grep -qE "_test\.go:[0-9]+: ${check}:" "${work}/out"; then
			cp "${work}/out" "${here}/logs/plain-${bug}.txt"
			found="run ${i}"
			break
		fi
	done
	echo "plain-${bug}: ${found:-no run failed on that check}: $(grep -m1 '_test.go' "${here}/logs/plain-${bug}.txt" 2>/dev/null)"
done
