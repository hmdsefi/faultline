#!/bin/bash
# Writes a replication module built on goroutines and net, with a flaky test and no faultline test.
exec bash "$(dirname "$0")/../testdata/workspace.sh" repl
