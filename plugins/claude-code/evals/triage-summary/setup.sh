#!/bin/bash
# Writes the artifact directory of the Get started failing seed into the workspace.
exec bash "$(dirname "$0")/../testdata/workspace.sh" artifact
