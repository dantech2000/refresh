#!/bin/sh
# run.sh starts fakefleet, runs a refresh binary against it with the
# environment it prints, and stops fakefleet when the binary exits.
#
#   tools/fakefleet/run.sh <fakefleet binary> <refresh binary> [refresh args...]
#
# FAKEFLEET_FLAGS holds extra flags for fakefleet, such as "-speed 10".
set -eu

fakefleet=$1
refresh=$2
shift 2

dir=$(mktemp -d)
env="$dir/fleet.env"
trap 'kill "$pid" 2>/dev/null; wait "$pid" 2>/dev/null; rm -rf "$dir"' EXIT INT TERM

# shellcheck disable=SC2086 # FAKEFLEET_FLAGS is a list of flags
"$fakefleet" -dir "$dir" ${FAKEFLEET_FLAGS:-} > "$env" &
pid=$!

# The environment ends with REFRESH_EKS_REGIONS; wait for it.
until grep -q REFRESH_EKS_REGIONS "$env" 2>/dev/null; do
  if ! kill -0 "$pid" 2>/dev/null; then
    echo "run.sh: fakefleet exited before it served" >&2
    exit 1
  fi
  sleep 0.1
done

# shellcheck disable=SC1090 # the file fakefleet wrote
. "$env"
"$refresh" "$@"
