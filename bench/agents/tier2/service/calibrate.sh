#!/usr/bin/env bash
# calibrate.sh: runs tier 2's calibration checks against the ten modules (the reference and the
# candidates trunc, sync, fwd and epoch, in both versions) and writes calibration.md next to this
# script. The rows "tells" and "fix experiments" are checked by hand; it lists them as pending.
# Exit 0 when every other row holds, 1 when one fails, 2 on a setup error. faultline comes from the
# module cache at the version prepare.sh requires. Needs python3 (trigger.py). No model calls.
#
# Optional: VISIBLE_RUNS (100), HIDDEN_RUNS (10), PARALLEL (12) plain visible runs at a time,
# SEEDS (2000) hidden seeds per count, REF_SEEDS (20000), TRIGGER_SEEDS (60) failing seeds whose
# traces trigger.py reads.
set -uo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Internal: one plain visible run, called through xargs.
if [[ "${1:-}" == __run ]]; then
	cd "$2/m" && "$2/t.test" -test.run '^TestLeaseKV$' -test.count=1 >"$2/out.$3" 2>&1
	echo $? >"$2/rc.$3"
	exit 0
fi

for tool in go python3; do
	if ! command -v "$tool" >/dev/null 2>&1; then
		echo "calibrate.sh: ${tool} not found" >&2
		exit 2
	fi
done
unset GOROOT
while IFS='=' read -r name _; do
	unset "$name"
done < <(env | grep '^FAULTLINE_')
export GOMEMLIMIT="${GOMEMLIMIT:-1GiB}"
VISIBLE_RUNS="${VISIBLE_RUNS:-100}"
HIDDEN_RUNS="${HIDDEN_RUNS:-10}"
PARALLEL="${PARALLEL:-12}"
SEEDS="${SEEDS:-2000}"
REF_SEEDS="${REF_SEEDS:-20000}"
TRIGGER_SEEDS="${TRIGGER_SEEDS:-60}"
bugs=(trunc sync fwd epoch)
modules=(reference "${bugs[@]}")
started=$(date +%s)

work="$(mktemp -d "${TMPDIR:-/tmp}/leasekv-calibrate-XXXXXX")" || exit 2
trap 'chmod -R u+w "$work" 2>/dev/null; rm -rf "$work"' EXIT
rows="${work}/rows.md"
details="${work}/details.md"
: >"$rows"
: >"$details"
failed=0

# row <holds:yes|no|pending> <check> <required> <result>
row() {
	printf '| %s | %s | %s | %s |\n' "$2" "$3" "$4" "$1" >>"$rows"
	echo "[$1] $2: $4"
	[[ "$1" == yes || "$1" == pending ]] || failed=1
}
detail() { printf '%s\n' "$*" >>"$details"; }

# designed <bug> <check>: whether check is a designed symptom of the candidate.
designed() {
	case "$1:$2" in
	trunc:"reads are fresh" | trunc:"acked appends survive" | sync:"reads are fresh" | fwd:"every op completes" | \
		epoch:"reads are fresh" | epoch:"every op completes") return 0 ;;
	esac
	return 1
}
expect() {
	case "$1" in
	trunc) echo '"reads are fresh" or "acked appends survive"' ;;
	sync) echo '"reads are fresh"' ;;
	fwd) echo '"every op completes"' ;;
	epoch) echo '"reads are fresh" or "every op completes"' ;;
	esac
}
# checks_in <file>: the failed check names in a go test log, one per line: faultline's first,
# then plain's (a log holds only one kind).
checks_in() {
	grep -oE '(invariant|final check) "[^"]+" (violated|failed)' "$1" | sed -E 's/^[a-z ]+ "([^"]+)".*$/\1/'
	grep -oE '_test\.go:[0-9]+: [a-z ]+:' "$1" | grep -v ': faultline:$' | sed -E 's/^.*_test\.go:[0-9]+: ([a-z ]+):$/\1/'
}
# tally <file of check names>: "check: n; check: n".
tally() { sort "$1" | uniq -c | sort -rn | sed -E 's/^ *([0-9]+) (.*)$/\2: \1/' | paste -sd ';' - | sed 's/;/; /g'; }

echo "== prepare"
for v in plain faultline; do
	for m in "${modules[@]}"; do
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
	for m in "${modules[@]}"; do
		d="${work}/${v}-${m}"
		f="$(cd "$d" && gofmt -l .)"
		if [[ -n "$f" ]] || ! (cd "$d" && go vet ./... >"${work}/vet.log" 2>&1); then
			bad="${bad} ${v}/${m}"
		fi
	done
done
row "$([[ -z "$bad" ]] && echo yes || echo no)" "gofmt and go vet" "clean in all ten modules (prepared copies)" "${bad:-clean}"

dirs=()
for v in plain faultline; do
	for m in "${modules[@]}"; do
		dirs+=("${here}/testdata/${v}/${m}")
	done
done
words="$(grep -rniwE 'bug|bugs|benchmark|hidden|variant|planted|candidate|decoy|calibrat\w*' "${dirs[@]}" | wc -l | tr -d ' ')"
# The clone path counts only as a whole folder name: a clone at /tmp/faultline does not match
# /tmp/faultline-1001/ in the logs.
repo="$(cd "${here}/../../../.." && pwd)"
paths="$(grep -rnE "/Users/|/home/|/private/|faultline-bench|${repo}([^[:alnum:]_.@+~%=#-]|\$)|/bench/agents" "${dirs[@]}" | wc -l | tr -d ' ')"
row "$([[ "$words" == 0 && "$paths" == 0 ]] && echo yes || echo no)" "words and paths in the modules" \
	"no bug, benchmark, hidden, variant, planted, candidate, decoy, calibrat*; no path of the repository, the benchmark or its cache" \
	"${words} word hits, ${paths} path hits"

same=yes
for v in plain faultline; do
	for t in "${here}/testdata/${v}/reference/"*_test.go; do
		for m in "${bugs[@]}"; do
			cmp -s "$t" "${here}/testdata/${v}/${m}/$(basename "$t")" || same=no
		done
	done
	for m in "${bugs[@]}"; do
		[[ "$(cd "${here}/testdata/${v}/${m}" && ls *_test.go)" == "$(cd "${here}/testdata/${v}/reference" && ls *_test.go)" ]] || same=no
	done
done
row "$same" "visible test files" "byte-identical in the reference and the four candidates of each version" \
	"$([[ $same == yes ]] && echo identical || echo differ)"

ptest="$(grep -hE '^(diff|\+\+\+|---) ' "${here}"/patches/*.diff | grep -c '_test\.go')"
pcount="$(ls "${here}"/patches/*.diff 2>/dev/null | wc -l | tr -d ' ')"
stale=""
for v in plain faultline; do
	for m in "${bugs[@]}"; do
		(cd "${here}/testdata/${v}" && diff -ru reference "$m" | grep -vE '^(\+\+\+|---) ') >"${work}/p1"
		grep -vE '^(\+\+\+|---) ' "${here}/patches/${v}-${m}.diff" >"${work}/p2"
		cmp -s "${work}/p1" "${work}/p2" || stale="${stale} ${v}-${m}"
		detail "- patch ${v}-${m}: $(grep -cE '^\+[^+]' "${here}/patches/${v}-${m}.diff") added, $(grep -cE '^-[^-]' "${here}/patches/${v}-${m}.diff") removed lines, files: $(grep -E '^diff ' "${here}/patches/${v}-${m}.diff" | awk '{print $NF}' | xargs -n1 basename | paste -sd, -)."
	done
done
row "$([[ "$ptest" == 0 && "$pcount" == 8 && -z "$stale" ]] && echo yes || echo no)" "patches" \
	"eight patches equal to diff -ru reference <candidate>, none changes a test file" \
	"${pcount} patches, ${ptest} test-file hunks${stale:+, stale:${stale}}"

nologs=""
for v in plain faultline; do
	for m in "${bugs[@]}"; do
		l="${here}/logs/${v}-${m}.txt"
		if [[ ! -s "$l" ]] || grep -qE "/Users/|/home/|/private/|/var/folders/|faultline-bench|${repo}([^[:alnum:]_.@+~%=#-]|\$)" "$l"; then
			nologs="${nologs} ${v}-${m}"
		elif [[ $v == faultline ]] && ! grep -q 'artifacts: /tmp/faultline-1001/' "$l"; then
			nologs="${nologs} ${v}-${m}"
		elif ! designed "$m" "$(checks_in "$l" | head -1)"; then
			nologs="${nologs} ${v}-${m}"
		fi
	done
done
row "$([[ -z "$nologs" ]] && echo yes || echo no)" "logs" \
	"eight logs of real failing runs on the designed check; no path of the repository, the benchmark or its cache; faultline artifacts under /tmp/faultline-1001/" \
	"${nologs:-eight logs, clean}"

# hidden <version> <dir> <log>: runs hidden.sh; prints "<exit> <seconds>".
hidden() {
	local s=$(date +%s)
	"${here}/hidden.sh" "$1" "$2" >"$3" 2>&1
	local rc=$?
	echo "$rc $(($(date +%s) - s))"
}

# seeds <module dir> <count> <base test name> <world> <out>: runs TestLeaseKVSeeds (faultline/extra)
# in a copy of the module with KeepGoing. Writes the failing seeds to <out>.seeds and their first
# failed checks to <out>.checks; prints "<exit> <failing> <seconds>".
seeds() {
	local d="${work}/seeds-m" s=$(date +%s)
	rm -rf "$d" && cp -R "$1" "$d" && cp "${here}/testdata/faultline/extra/extra_test.go" "${d}/zz_extra_test.go"
	(cd "$d" && FAULTLINE_ARTIFACTS=off LEASEKV_SEEDS="$2" LEASEKV_BASE="$3" LEASEKV_WORLD="$4" \
		go test -count=1 -run '^TestLeaseKVSeeds$' -timeout 60m . >"$5" 2>&1)
	local rc=$?
	grep -oE -- '--- FAIL: TestLeaseKVSeeds/seed=0x[0-9a-f]+' "$5" | sed 's/.*seed=//' >"$5.seeds"
	grep -oE '(invariant|final check) "[^"]+" (violated|failed)' "$5" | sed -E 's/^[a-z ]+ "([^"]+)".*$/\1/' >"$5.checks"
	echo "$rc $(wc -l <"$5.seeds" | tr -d ' ') $(($(date +%s) - s))"
}

echo "== faultline reference"
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
read -r src sn ss < <(seeds "$d" "$REF_SEEDS" TestLeaseKV base "${work}/ref20k.log")
row "$([[ $src == 0 && $sn == 0 ]] && echo yes || echo no)" "faultline reference, ${REF_SEEDS} seeds" \
	"${REF_SEEDS} seeds of TestLeaseKV's base pass with KeepGoing" "${sn} failing, exit ${src} (${ss}s)"

echo "== robustness"
for w in crashes long pause tail; do
	case $w in
	crashes) what="crashes every 500 ms, partitions every 300 ms" ;;
	long) what="20 s runs, crashes every 700 ms" ;;
	pause) what="a pause rule on s1, s2, st1, st2 (10 to 400 ms)" ;;
	tail) what="tail delays on every link (2 % of messages, up to 40 ms)" ;;
	esac
	read -r src sn ss < <(seeds "$d" "$SEEDS" TestLeaseKVHidden "$w" "${work}/robust-${w}.log")
	row "$([[ $src == 0 && $sn == 0 ]] && echo yes || echo no)" "faultline reference, robustness: ${what}" \
		"the ${SEEDS} hidden seeds pass with KeepGoing" "${sn} failing, exit ${src} (${ss}s)"
done

echo "== faultline candidates"
for m in "${bugs[@]}"; do
	d="${work}/faultline-${m}"
	(cd "$d" && FAULTLINE_ARTIFACTS="${work}/art" go test -count=1 ./... >"${work}/fv-${m}.log" 2>&1)
	vrc=$?
	vcheck="$(checks_in "${work}/fv-${m}.log" | head -1)"
	seed="$(grep -oE 'seed=0x[0-9a-f]+ \(' "${work}/fv-${m}.log" | head -1 | grep -oE '0x[0-9a-f]+')"
	notrun="$(grep -oE '[0-9]+ of [0-9]+ seeds not run' "${work}/fv-${m}.log" | head -1 | cut -d' ' -f1)"
	read -r hrc hs < <(hidden faultline "$d" "${work}/fh-${m}.log")
	hcheck="$(checks_in "${work}/fh-${m}.log" | head -1)"
	ok=no
	if [[ $vrc != 0 && $hrc == 1 ]] && designed "$m" "$vcheck" && designed "$m" "$hcheck"; then ok=yes; fi
	row "$ok" "faultline ${m}" "visible test fails within 100 seeds on $(expect "$m"); hidden check fails (exit 1) on it" \
		"visible: \"${vcheck}\" at seed ${seed} (seed $((100 - ${notrun:-100})) of 100); hidden: exit ${hrc}, \"${hcheck}\" (${hs}s)"
	detail "- faultline ${m}: hidden check ${hs}s."

	read -r src sn ss < <(seeds "$d" "$SEEDS" TestLeaseKVHidden base "${work}/count-${m}.log")
	row "$([[ $sn -ge 20 ]] && echo yes || echo no)" "faultline ${m}, ${SEEDS} hidden seeds" \
		"at least 20 fail with KeepGoing" "${sn} of ${SEEDS} fail ($(tally "${work}/count-${m}.log.checks")) (${ss}s)"

	# trigger: the traces of the first failing seeds, read by trigger.py.
	list="$(head -n "$TRIGGER_SEEDS" "${work}/count-${m}.log.seeds" | paste -sd, -)"
	t="${work}/trig-${m}"
	rm -rf "$t" && cp -R "$d" "$t" && cp "${here}/testdata/faultline/extra/extra_test.go" "${t}/zz_extra_test.go"
	(cd "$t" && FAULTLINE_ARTIFACTS="${t}-art" FAULTLINE_SEED_LIST="$list" LEASEKV_SEEDS=1 LEASEKV_BASE=TestLeaseKVHidden \
		go test -count=1 -run '^TestLeaseKVSeeds$' -timeout 60m . >"${t}.log" 2>&1)
	python3 "${here}/trigger.py" "$m" "${t}-art"/*/TestLeaseKVSeeds/*/trace.jsonl >"${t}.txt" 2>"${t}.err"
	matches="$(grep -c ' match: ' "${t}.txt")"
	read_traces="$(wc -l <"${t}.txt" | tr -d ' ')"
	row "$([[ $matches -ge 3 ]] && echo yes || echo no)" "trigger ${m}" \
		"in 3 failing faultline seeds the trace shows the designed trigger, in order (trigger.py)" \
		"${matches} of ${read_traces} failing hidden seeds read show it; first three in the notes"
	detail "- trigger ${m} (${matches} of ${read_traces}; record numbers are trace seq):"
	grep ' match: ' "${t}.txt" | head -3 | sed 's/^/  - /' >>"$details"
	rm -rf "$t" "${t}-art"
done

echo "== plain"
# visible <module> <runs> [race]: runs the plain visible test; writes the first failed check of
# each run, or "pass", to ${work}/counts.
visible() {
	local d="$1" n="$2" race="${3:-}" r="${work}/runs"
	: >"${work}/counts"
	rm -rf "$r" && mkdir -p "$r" && cp -R "$d" "${r}/m"
	if ! (cd "${r}/m" && go test ${race:+-race} -c -o "${r}/t.test" .); then
		echo "build failed" >>"${work}/counts"
		return 1
	fi
	seq 1 "$n" | xargs -P "$PARALLEL" -n 1 "${here}/calibrate.sh" __run "$r"
	local i c
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

d="${work}/plain-reference"
visible "$d" "$VISIBLE_RUNS"
p="$(grep -c '^pass$' "${work}/counts")"
row "$([[ $p == "$VISIBLE_RUNS" ]] && echo yes || echo no)" "plain reference, visible" "passes ${VISIBLE_RUNS} of ${VISIBLE_RUNS} runs" "$(tally "${work}/counts")"
visible "$d" 20 race
p="$(grep -c '^pass$' "${work}/counts")"
row "$([[ $p == 20 ]] && echo yes || echo no)" "plain reference, visible under -race" "passes 20 of 20 runs" "$(tally "${work}/counts")"
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

for m in "${bugs[@]}"; do
	d="${work}/plain-${m}"
	visible "$d" "$VISIBLE_RUNS"
	f=0
	while read -r c; do
		designed "$m" "$c" && f=$((f + 1))
	done <"${work}/counts"
	row "$([[ $f -ge 10 ]] && echo yes || echo no)" "plain ${m}, visible" \
		"fails in at least 10 of ${VISIBLE_RUNS} runs on $(expect "$m"); rate recorded" \
		"${f} of ${VISIBLE_RUNS} on the designed check; all runs: $(tally "${work}/counts")"
	fails=0
	times=""
	: >"${work}/hchecks"
	for i in $(seq 1 "$HIDDEN_RUNS"); do
		read -r hrc hs < <(hidden plain "$d" "${work}/ph-${m}.log")
		[[ $hrc == 1 ]] && fails=$((fails + 1))
		times="${times} ${hs}"
		checks_in "${work}/ph-${m}.log" >>"${work}/hchecks"
		detail "- plain ${m} hidden invocation ${i}: exit ${hrc}, $(clusters "${work}/ph-${m}.log" "$hrc") clusters failed, ${hs}s."
	done
	row "$([[ $fails == "$HIDDEN_RUNS" ]] && echo yes || echo no)" "plain ${m}, hidden" \
		"fails ${HIDDEN_RUNS} of ${HIDDEN_RUNS} invocations (exit 1)" \
		"${fails} of ${HIDDEN_RUNS} failed; failed clusters by check: $(tally "${work}/hchecks"); seconds:${times}"
done

echo "== hidden.sh: test files in the checked module are ignored; exit 2 means it does not compile"
for v in plain faultline; do
	for m in reference trunc; do
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
		[[ $m == trunc ]] && want=1
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

for m in "${bugs[@]}"; do
	row pending "tells ${m}" "a reader who sees only the candidate and its logs finds nothing that states or points at the fix" \
		"checked by hand, not by this script"
done
row pending "fix experiments" "correct fixes pass and named wrong fixes fail, in both versions" \
	"checked by hand after the screen, for the three candidates it keeps"

{
	echo "# leasekv tier 2 calibration"
	echo
	echo "Written by calibrate.sh on $(date -u '+%Y-%m-%d %H:%M UTC') with $(go version | cut -d' ' -f3-), faultline from"
	echo "the module cache at the version prepare.sh requires, in $(($(date +%s) - started)) s. Visible runs per plain module:"
	echo "${VISIBLE_RUNS}, ${PARALLEL} at a time. Hidden invocations per plain module: ${HIDDEN_RUNS}, one at a time. A plain"
	echo "hidden invocation runs up to 600 clusters of 7 s under -race, in batches of 60 that run in parallel, and stops"
	echo "after the first batch with a failing cluster. faultline seed counts use TestLeaseKVSeeds (faultline/extra) with"
	echo "KeepGoing: the ${SEEDS} hidden seeds are those of TestLeaseKVHidden. Run times include the build."
	echo
	echo "Result: $([[ $failed == 0 ]] && echo 'every row holds, except the pending ones' || echo 'at least one row FAILS')."
	echo
	echo "| Check | Required | Result | Holds |"
	echo "|---|---|---|---|"
	cat "$rows"
	echo
	echo "## Notes: run times, patches, triggers"
	echo
	cat "$details"
} >"${here}/calibration.md"
echo "wrote ${here}/calibration.md"
exit "$failed"
