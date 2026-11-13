#!/bin/sh
set -eu
exec /usr/bin/python3 -m unittest discover -s "$(dirname "$0")" -p 'test_*.py' "$@"
