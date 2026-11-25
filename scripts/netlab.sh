#!/usr/bin/env bash
# Run the network lab (internal/netlab) as root: on this machine if it is Linux,
# otherwise in the podman VM. Arguments go to the test binary:
#
#   scripts/netlab.sh                             # everything
#   scripts/netlab.sh -test.run SameLAN -test.v   # one test
#
# HOP_NETLAB_BIN=<dir> tests the hop-server and hop-agent in that directory
# instead of this tree's. See docs/wiki/concepts/netlab.md.
set -euo pipefail
cd "$(dirname "$0")/.."

out=$(mktemp -d)
trap 'rm -rf "$out"' EXIT
arch=$(uname -m); case "$arch" in x86_64) arch=amd64 ;; aarch64) arch=arm64 ;; esac
export CGO_ENABLED=0 GOOS=linux GOARCH=$arch
for b in server agent; do
	if [ -n "${HOP_NETLAB_BIN:-}" ]; then
		cp "$HOP_NETLAB_BIN/hop-$b" "$out/"
	else
		go build -mod=vendor -o "$out/hop-$b" "./cmd/$b"
	fi
done
go test -mod=vendor -c -o "$out/netlab.test" ./internal/netlab
[ $# -gt 0 ] || set -- -test.v

run='cd "$1" && shift && exec env HOP_NETLAB=1 HOP_NETLAB_BIN="$PWD" ./netlab.test "$@"'
if [ "$(uname)" = Linux ]; then
	sudo sh -c "$run" sh "$out" -test.timeout 60m "$@"
else
	# Copied, because programs start slowly from the Mac's file system.
	dir=/var/tmp/hop-netlab
	COPYFILE_DISABLE=1 tar --no-xattrs -C "$out" -cf - . | podman machine ssh "rm -rf $dir && mkdir $dir && tar -xf - -C $dir"
	code=0
	podman machine ssh "sudo sh -c $(printf %q "$run") sh $dir -test.timeout 60m $(printf '%q ' "$@")" || code=$?
	podman machine ssh "rm -rf $dir"
	exit $code
fi
