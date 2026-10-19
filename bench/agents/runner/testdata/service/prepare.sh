#!/bin/sh
# Stub of prepare.sh <src> <dest>: copies the variant and writes its go.mod, as the real one does
# for a variant that does not import faultline.
set -eu
cp -R "$1" "$2"
printf 'module example.com/leasekv\n\ngo 1.26\n' >"$2/go.mod"
