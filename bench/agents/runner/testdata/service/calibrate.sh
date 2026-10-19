#!/bin/sh
# Stub of calibrate.sh: prints one row of the table and the faultline settings it was given.
echo "| row | holds |"
echo "FAULTLINE_ settings: $(env | grep -c '^FAULTLINE_')"
