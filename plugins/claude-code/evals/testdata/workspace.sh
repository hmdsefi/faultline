#!/bin/bash
# workspace.sh PART...: writes eval fixtures into the current directory, the run's workspace.
#
#   kv        the key-value module from Get started (example.com/kv), with its bug, requiring
#             faultline v0.1.1
#   artifact  the artifact directory of its failing seed 0x287372ab06f1482e, at
#             artifacts/example.com_kv/TestKV/287372ab06f1482e
#   repl      a replication module (example.com/repl) built on goroutines and net, with a
#             flaky test and no faultline test
#
# The artifact was recorded with the module in /home/dev/kv and the artifact root
# /tmp/faultline-501; the paths in report.json and report.txt are rewritten to the workspace.
# Its timeline.txt keeps the last 63 records, with the header faultline writes when it drops the
# earlier ones.
set -euo pipefail
src=$(cd "$(dirname "$0")" && pwd)
for part in "$@"; do
	case $part in
	kv)
		cp "$src/kv/kv.go" "$src/kv/kv_test.go" .
		printf 'module example.com/kv\n\ngo 1.26.0\n\nrequire github.com/hmdsefi/faultline v0.1.1\n' >go.mod
		;;
	artifact)
		dir=artifacts/example.com_kv/TestKV/287372ab06f1482e
		mkdir -p "$dir"
		cp "$src/artifact/"* "$dir/"
		for f in report.json report.txt; do
			sed -e "s|/home/dev/kv|$PWD|g" -e "s|/tmp/faultline-501|$PWD/artifacts|g" "$src/artifact/$f" >"$dir/$f"
		done
		;;
	repl)
		cp "$src/repl/repl.go" "$src/repl/repl_test.go" .
		printf 'module example.com/repl\n\ngo 1.26.0\n' >go.mod
		;;
	*)
		echo "workspace.sh: unknown part \"$part\"; want kv, artifact or repl" >&2
		exit 2
		;;
	esac
done
