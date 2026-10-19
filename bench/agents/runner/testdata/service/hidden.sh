#!/bin/sh
# Stub of hidden.sh <plain|faultline> <dir>: passes when kv.go holds the fix. It breaks the
# contract on purpose (it writes into <dir>), so the tests can show the runner passes it a copy.
# FAKE_HIDDEN_SLEEP=<s> makes it slow, for the timeout test.
set -u
case "${1:-}" in plain|faultline) ;; *) echo "bad version: ${1:-}"; exit 3 ;; esac
echo leak > "$2/hidden_leak.txt"
if [ -n "${FAKE_HIDDEN_SLEEP:-}" ]; then sleep "$FAKE_HIDDEN_SLEEP"; fi
if grep -q 'Fixed = true' "$2/kv.go"; then echo "ok  hidden ($1)"; exit 0; fi
echo "--- FAIL: TestHidden ($1)"
exit 1
