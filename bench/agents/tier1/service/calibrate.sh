#!/usr/bin/env bash
# calibrate.sh: runs tier 1's calibration checks against the eight modules (the reference and the
# bugs b1, b2 and b3, in both versions) and writes calibration.md next to this script. Exit 0 when
# every row holds, 1 when a row fails, 2 on a setup error. faultline comes from the module cache at
# the version prepare.sh requires. No model calls.
#
# Optional: VISIBLE_RUNS (100), HIDDEN_RUNS (10), PARALLEL (12) plain visible runs at a time.
set -uo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Internal: one plain visible run, called through xargs.
if [[ "${1:-}" == __run ]]; then
	cd "$2/m" && "$2/t.test" -test.run '^TestLeaseKV$' -test.count=1 >"$2/out.$3" 2>&1
	echo $? >"$2/rc.$3"
	exit 0
fi

if ! command -v go >/dev/null 2>&1; then
	echo "calibrate.sh: go not found" >&2
	exit 2
fi
unset GOROOT
while IFS='=' read -r name _; do
	unset "$name"
done < <(env | grep '^FAULTLINE_')
export GOMEMLIMIT="${GOMEMLIMIT:-1GiB}"
VISIBLE_RUNS="${VISIBLE_RUNS:-100}"
HIDDEN_RUNS="${HIDDEN_RUNS:-10}"
PARALLEL="${PARALLEL:-12}"

work="$(mktemp -d "${TMPDIR:-/tmp}/leasekv-calibrate-XXXXXX")" || exit 2
trap 'chmod -R u+w "$work" 2>/dev/null; rm -rf "$work"' EXIT
rows="${work}/rows.md"
details="${work}/details.md"
: >"$rows"
: >"$details"
failed=0

# row <holds:yes|no> <check> <required> <result>
row() {
	printf '| %s | %s | %s | %s |\n' "$2" "$3" "$4" "$1" >>"$rows"
	echo "[$1] $2: $4"
	[[ "$1" == yes ]] || failed=1
}
detail() { printf '%s\n' "$*" >>"$details"; }

allowed() { # allowed <bug> <check>
	case "$1:$2" in
	b1:"reads are fresh" | b1:"acked appends survive" | b2:"no duplicate appends" | b3:"reads are fresh") return 0 ;;
	esac
	return 1
}
expect() {
	case "$1" in
	b1) echo '"reads are fresh" or "acked appends survive"' ;;
	b2) echo '"no duplicate appends"' ;;
	b3) echo '"reads are fresh"' ;;
	esac
}
# checks_in <file>: the failed check names in a go test log, one per line.
checks_in() {
	grep -oE '(invariant|final check) "[^"]+"' "$1" | sed -E 's/^[a-z ]+ "(.*)"$/\1/'
	grep -oE '_test\.go:[0-9]+: [a-z ]+:' "$1" | sed -E 's/^.*: ([a-z ]+):$/\1/'
}

echo "== prepare"
for v in plain faultline; do
	for m in reference b1 b2 b3; do
		if ! "${here}/prepare.sh" "${here}/testdata/${v}/${m}" "${work}/${v}-${m}" >"${work}/prepare.log" 2>&1; then
			cat "${work}/prepare.log" >&2
			echo "calibrate.sh: prepare.sh failed for ${v}/${m}" >&2
			exit 2
		fi
	done
done

echo "== static checks"
bad=""
for v in plain faultline; do
	for m in reference b1 b2 b3; do
		d="${work}/${v}-${m}"
		f="$(cd "$d" && gofmt -l .)"
		if [[ -n "$f" ]] || ! (cd "$d" && go vet ./... >"${work}/vet.log" 2>&1); then
			bad="${bad} ${v}/${m}"
		fi
	done
done
row "$([[ -z "$bad" ]] && echo yes || echo no)" "gofmt and go vet" "clean in all eight modules (prepared copies)" "${bad:-clean}"

words="$(grep -rniwE 'bug|bugs|benchmark|hidden|variant|planted|calibrat\w*' "${here}"/testdata/{plain,faultline}/{reference,b1,b2,b3} | wc -l | tr -d ' ')"
# The clone path counts only as a whole folder name: a clone at /tmp/faultline does not match
# /tmp/faultline-1001/ in the logs.
repo="$(cd "${here}/../../../.." && pwd)"
paths="$(grep -rnE "/Users/|/home/|/private/|faultline-bench|${repo}([^[:alnum:]_.@+~%=#-]|\$)|/bench/agents" "${here}"/testdata/{plain,faultline}/{reference,b1,b2,b3} | wc -l | tr -d ' ')"
row "$([[ "$words" == 0 && "$paths" == 0 ]] && echo yes || echo no)" "words and paths in the modules" \
	"no bug, benchmark, hidden, variant, planted, calibrat*; no path of the repository, the benchmark or its cache" "${words} word hits, ${paths} path hits"

same=yes
for v in plain faultline; do
	for t in "${here}/testdata/${v}/reference/"*_test.go; do
		for m in b1 b2 b3; do
			cmp -s "$t" "${here}/testdata/${v}/${m}/$(basename "$t")" || same=no
		done
	done
done
row "$same" "visible test files" "byte-identical in reference, b1, b2, b3 of each version" "$([[ $same == yes ]] && echo identical || echo differ)"

ptest="$(grep -hE '^(diff|\+\+\+|---) ' "${here}"/patches/*.diff | grep -c '_test\.go')"
pcount="$(ls "${here}"/patches/*.diff 2>/dev/null | wc -l | tr -d ' ')"
row "$([[ "$ptest" == 0 && "$pcount" == 6 ]] && echo yes || echo no)" "patches" "six patches, none changes a test file" "${pcount} patches, ${ptest} test-file hunks"

nologs=""
for v in plain faultline; do
	for m in b1 b2 b3; do
		l="${here}/logs/${v}-${m}.txt"
		if [[ ! -s "$l" ]] || grep -qE "/Users/|/home/|/private/|/var/folders/|faultline-bench|${repo}([^[:alnum:]_.@+~%=#-]|\$)" "$l"; then
			nologs="${nologs} ${v}-${m}"
		fi
	done
done
row "$([[ -z "$nologs" ]] && echo yes || echo no)" "logs" "six logs, no path of the repository, the benchmark or its cache" "${nologs:-six logs, clean}"

# hidden <version> <dir> <log>: runs hidden.sh; prints "<exit> <seconds>".
hidden() {
	local s=$(date +%s)
	"${here}/hidden.sh" "$1" "$2" >"$3" 2>&1
	local rc=$?
	echo "$rc $(($(date +%s) - s))"
}

echo "== faultline"
d="${work}/faultline-reference"
(cd "$d" && go test -count=1 ./... >"${work}/fv.log" 2>&1)
v1=$?
(cd "$d" && FAULTLINE_CHECK_DETERMINISM=1 go test -count=1 ./... >"${work}/fd.log" 2>&1)
v2=$?
read -r hrc hs < <(hidden faultline "$d" "${work}/fh.log")
row "$([[ $v1 == 0 && $v2 == 0 && $hrc == 0 ]] && echo yes || echo no)" "faultline reference" \
	"visible test passes, also with FAULTLINE_CHECK_DETERMINISM=1; hidden check passes" \
	"visible exit ${v1}, determinism exit ${v2}, hidden exit ${hrc} (${hs}s)"
detail "- faultline reference: hidden check ${hs}s."
for m in b1 b2 b3; do
	d="${work}/faultline-${m}"
	(cd "$d" && FAULTLINE_ARTIFACTS="${work}/art" go test -count=1 ./... >"${work}/fv-${m}.log" 2>&1)
	vrc=$?
	vcheck="$(checks_in "${work}/fv-${m}.log" | head -1)"
	seed="$(grep -oE 'seed=0x[0-9a-f]+ \(' "${work}/fv-${m}.log" | head -1 | grep -oE '0x[0-9a-f]+')"
	stopped="$(grep -oE '[0-9]+ of [0-9]+ seeds not run' "${work}/fv-${m}.log" | head -1)"
	read -r hrc hs < <(hidden faultline "$d" "${work}/fh-${m}.log")
	hcheck="$(checks_in "${work}/fh-${m}.log" | head -1)"
	ok=no
	if [[ $vrc != 0 && $hrc == 1 ]] && allowed "$m" "$vcheck" && allowed "$m" "$hcheck"; then ok=yes; fi
	row "$ok" "faultline ${m}" "visible test fails on $(expect "$m"); hidden check fails (exit 1) on it" \
		"visible: \"${vcheck}\" at seed ${seed} (${stopped}); hidden: exit ${hrc}, \"${hcheck}\" (${hs}s)"
	detail "- faultline ${m}: hidden check ${hs}s."
done

echo "== plain"
# visible <module> <runs> [race]: runs the plain visible test; writes counts to ${work}/counts.
visible() {
	local d="$1" n="$2" race="${3:-}" r="${work}/runs"
	: >"${work}/counts"
	rm -rf "$r" && mkdir -p "$r" && cp -R "$d" "${r}/m"
	if ! (cd "${r}/m" && go test ${race:+-race} -c -o "${r}/t.test" .); then
		echo "build failed" >>"${work}/counts"
		return 1
	fi
	seq 1 "$n" | xargs -P "$PARALLEL" -n 1 "${here}/calibrate.sh" __run "$r"
	local i
	for i in $(seq 1 "$n"); do
		if [[ "$(cat "${r}/rc.${i}")" == 0 ]]; then
			echo pass >>"${work}/counts"
		else
			c="$(checks_in "${r}/out.${i}" | head -1)"
			echo "${c:-other}" >>"${work}/counts"
		fi
	done
}
# clusters <log> <exit>: "<failed> of <run>" clusters of a plain hidden check. A failing batch
# stops it, so the clusters run are 60 per batch up to the last batch with a failure.
clusters() {
	local failed last
	failed="$(grep -cE -- '--- FAIL: TestLeaseKVHidden/batch=[0-9]+/run=' "$1")"
	last="$(grep -oE -- '--- FAIL: TestLeaseKVHidden/batch=[0-9]+' "$1" | grep -oE '[0-9]+$' | sort -n | tail -1)"
	if [[ "$2" == 0 ]]; then
		echo "${failed} of 600"
	else
		echo "${failed} of $((${last:-0} * 60))"
	fi
}
summary() { sort "${work}/counts" | uniq -c | sed -E 's/^ *([0-9]+) (.*)$/\2: \1/' | paste -sd ';' - | sed 's/;/; /g'; }

d="${work}/plain-reference"
visible "$d" "$VISIBLE_RUNS"
p="$(grep -c '^pass$' "${work}/counts")"
row "$([[ $p == "$VISIBLE_RUNS" ]] && echo yes || echo no)" "plain reference, visible" "passes ${VISIBLE_RUNS} of ${VISIBLE_RUNS} runs" "$(summary)"
visible "$d" 20 race
p="$(grep -c '^pass$' "${work}/counts")"
row "$([[ $p == 20 ]] && echo yes || echo no)" "plain reference, visible under -race" "passes 20 of 20 runs" "$(summary)"
pass=0
times=""
for i in $(seq 1 "$HIDDEN_RUNS"); do
	read -r hrc hs < <(hidden plain "$d" "${work}/ph.log")
	[[ $hrc == 0 ]] && pass=$((pass + 1))
	times="${times} ${hs}"
	detail "- plain reference hidden invocation ${i}: exit ${hrc}, $(clusters "${work}/ph.log" "$hrc") clusters failed, ${hs}s."
done
row "$([[ $pass == "$HIDDEN_RUNS" ]] && echo yes || echo no)" "plain reference, hidden" \
	"passes ${HIDDEN_RUNS} of ${HIDDEN_RUNS} invocations (600 clusters each, -race)" "${pass} of ${HIDDEN_RUNS} passed; seconds:${times}"

for m in b1 b2 b3; do
	d="${work}/plain-${m}"
	visible "$d" "$VISIBLE_RUNS"
	p="$(grep -c '^pass$' "${work}/counts")"
	f=$((VISIBLE_RUNS - p))
	badcheck="$(grep -v '^pass$' "${work}/counts" | sort -u | while read -r c; do allowed "$m" "$c" || echo "$c"; done)"
	row "$([[ $f -ge 1 && $p -ge 1 && -z "$badcheck" ]] && echo yes || echo no)" "plain ${m}, visible" \
		"fails at least once and passes at least once in ${VISIBLE_RUNS} runs, on $(expect "$m"); rate recorded" \
		"failure rate ${f}/${VISIBLE_RUNS} ($(summary))"
	fails=0
	times=""
	hbad=""
	for i in $(seq 1 "$HIDDEN_RUNS"); do
		read -r hrc hs < <(hidden plain "$d" "${work}/ph-${m}.log")
		[[ $hrc == 1 ]] && fails=$((fails + 1))
		times="${times} ${hs}"
		for c in $(checks_in "${work}/ph-${m}.log" | sort -u | tr ' ' '_'); do
			allowed "$m" "${c//_/ }" || hbad="${hbad} ${c}"
		done
		detail "- plain ${m} hidden invocation ${i}: exit ${hrc}, $(clusters "${work}/ph-${m}.log" "$hrc") clusters failed, ${hs}s."
	done
	row "$([[ $fails == "$HIDDEN_RUNS" && -z "$hbad" ]] && echo yes || echo no)" "plain ${m}, hidden" \
		"fails ${HIDDEN_RUNS} of ${HIDDEN_RUNS} invocations (exit 1), on $(expect "$m")" \
		"${fails} of ${HIDDEN_RUNS} failed${hbad:+, other checks:${hbad}}; seconds:${times}"
done

echo "== test files in the checked module are ignored; exit 2 means it does not compile"
for v in plain faultline; do
	for m in reference b1; do
		d="${work}/${v}-${m}-extra"
		cp -R "${work}/${v}-${m}" "$d"
		cat >"${d}/zz_extra_test.go" <<'GO'
package leasekv_test

import (
	"os"
	"testing"
)

func init() { panic("init in a test file") }

func TestMain(m *testing.M) { os.Exit(0) }
GO
		read -r hrc hs < <(hidden "$v" "$d" "${work}/extra.log")
		want=0
		[[ $m == b1 ]] && want=1
		row "$([[ $hrc == "$want" ]] && echo yes || echo no)" "${v} ${m} + TestMain(exit 0) + init(panic) in a test file" \
			"hidden.sh still exits ${want}" "exit ${hrc}"
	done
	d="${work}/${v}-syntax"
	cp -R "${work}/${v}-reference" "$d"
	printf 'package leasekv\n\nfunc broken( {\n' >"${d}/broken.go"
	read -r hrc hs < <(hidden "$v" "$d" "${work}/syntax.log")
	row "$([[ $hrc == 2 ]] && echo yes || echo no)" "${v} reference + a syntax error" "hidden.sh exits 2" "exit ${hrc}"
	d="${work}/${v}-vet"
	cp -R "${work}/${v}-reference" "$d"
	printf 'package leasekv\n\nimport "fmt"\n\nvar _ = fmt.Sprintf("%%d", "x")\n' >"${d}/vet.go"
	read -r hrc hs < <(hidden "$v" "$d" "${work}/vet.log")
	row "$([[ $hrc == 0 ]] && echo yes || echo no)" "${v} reference + a go vet finding in a non-test file" "hidden.sh exits 0 (vet is off)" "exit ${hrc}"
done

echo "== module setup is pinned"
for v in plain faultline; do
	d="${work}/${v}-modsetup"
	cp -R "${work}/${v}-reference" "$d"
	(cd "$d" && go mod edit -replace "github.com/hmdsefi/faultline=${work}/nowhere" -require "example.com/nowhere@v1.0.0")
	mkdir -p "${d}/vendor" && echo "# broken" >"${d}/vendor/modules.txt"
	printf 'go 1.26\n\nuse ./nowhere\n' >"${d}/go.work"
	read -r hrc hs < <(GOFLAGS=-mod=vendor hidden "$v" "$d" "${work}/modsetup.log")
	row "$([[ $hrc == 0 ]] && echo yes || echo no)" "${v} reference + its own go.mod replace and require, vendor/, go.work, GOFLAGS=-mod=vendor" \
		"hidden.sh exits 0 (reference go.mod and go.sum, GOWORK=off, GOFLAGS=-mod=mod)" "exit ${hrc}"
done

{
	echo "# leasekv calibration"
	echo
	echo "Written by calibrate.sh on $(date -u '+%Y-%m-%d %H:%M UTC') with $(go version | cut -d' ' -f3-), faultline from"
	echo "the module cache at the version prepare.sh requires. Visible runs per plain module: ${VISIBLE_RUNS},"
	echo "${PARALLEL} at a time. Hidden invocations per plain module: ${HIDDEN_RUNS}, one at a time. A plain hidden"
	echo "invocation runs up to 600 clusters of 7 s under -race, in batches of 60 that run in parallel, and"
	echo "stops after the first batch with a failing cluster. Run times include the build."
	echo
	echo "Result: $([[ $failed == 0 ]] && echo 'every row holds' || echo 'at least one row FAILS')."
	echo
	echo "| Check | Required | Result | Holds |"
	echo "|---|---|---|---|"
	cat "$rows"
	echo
	echo "## Hidden check run times"
	echo
	cat "$details"
} >"${here}/calibration.md"
echo "wrote ${here}/calibration.md"
exit "$failed"
