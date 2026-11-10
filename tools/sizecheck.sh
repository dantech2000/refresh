#!/bin/sh
# sizecheck.sh BINARY fails if BINARY, a stripped release-style build of
# refresh (-trimpath -ldflags "-s -w"), is larger than LIMIT_MB.
#
# The limit guards against a change that turns off the linker's dead-method
# elimination. That happens when the binary can reach a reflect call that
# looks up a method by a name not known at compile time, for example
# text/template executing a template (cobra does that after any
# Set*Template call). The linker then keeps every exported method of every
# type stored in an interface, and the binary more than doubles: 108 MB
# instead of 43 MB (linux/amd64, Go 1.26). If the check fails, look for
# such a call first: `go build -ldflags=-dumpdep ./cmd/refresh 2>&1 | grep
# -e '-> text/template.(\*Template).Execute$' -e '-> reflect.Value.Method'`.
#
# The limit is the largest release target (darwin/amd64, 44.4 MB in
# November 2026) plus about 15%. To raise it
# on purpose, for a new dependency that is worth its size, build every
# release target, set LIMIT_MB to the largest size plus about 15%, and
# write why in the commit message.
set -eu

LIMIT_MB=51

if [ $# -ne 1 ]; then
	echo "usage: $0 BINARY" >&2
	exit 2
fi
# A missing or unreadable file must fail: a failed redirect inside the
# pipeline below would give an empty size, and set -e does not see it.
if [ ! -f "$1" ] || [ ! -r "$1" ]; then
	echo "$1: no such readable file" >&2
	exit 1
fi
size=$(wc -c <"$1" | tr -d ' ')
case $size in
'' | *[!0-9]*)
	echo "$1: cannot read its size (got \"$size\")" >&2
	exit 1
	;;
esac
if [ "$size" -eq 0 ]; then
	echo "$1: empty file" >&2
	exit 1
fi
limit=$((LIMIT_MB * 1000 * 1000))
mb=$(awk -v s="$size" 'BEGIN { printf "%.1f", s / 1e6 }')
if [ "$size" -gt "$limit" ]; then
	echo "$1 is $mb MB, over the limit of $LIMIT_MB MB. See tools/sizecheck.sh." >&2
	exit 1
fi
echo "$1 is $mb MB (limit $LIMIT_MB MB)"
