#!/bin/bash
# Writes the Get started key-value module and its failing seed's artifact directory.
exec bash "$(dirname "$0")/../testdata/workspace.sh" kv artifact
