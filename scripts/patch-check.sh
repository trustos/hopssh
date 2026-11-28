#!/usr/bin/env bash
# Checks the vendor patches against a pristine vendor tree, built from the
# module cache without the network (go mod vendor -o):
#   1. each patch applies on its own;
#   2. all of them apply in order, and in reverse order, with no fuzz, and
#      give the same files both ways: no patch depends on another, so one can
#      be dropped without touching the others (patches/README.md);
#   3. the pristine tree with them is vendor/, every file of it: nothing
#      there was changed by hand.
# Run it after writing or changing a patch: make patch-check.
set -euo pipefail
cd "$(dirname "$0")/.."

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
GOFLAGS=-mod=mod GOPROXY=off go mod vendor -o "$tmp/pristine"

patches=(patches/*.patch)
# Every file a patch touches, relative to vendor/.
grep -h '^+++ b/' "${patches[@]}" | sed 's|^+++ b/||; s|[[:space:]].*$||' | sort -u > "$tmp/files"

copy() { # copy DIR: the touched files of the pristine tree into DIR
	mkdir -p "$1"
	while read -r f; do
		if [ -f "$tmp/pristine/$f" ]; then
			mkdir -p "$1/$(dirname "$f")"
			cp "$tmp/pristine/$f" "$1/$f"
		fi
	done < "$tmp/files"
}
apply() { # apply DIR PATCH
	(cd "$1" && patch -p1 --forward -F0 --silent < "$OLDPWD/$2" > "$tmp/out" 2>&1) || {
		echo "FAILED: $2 in $3"
		cat "$tmp/out"
		return 1
	}
	# Apple's patch keeps a .orig when a hunk lands at an offset, which two
	# patches of one file do in one of the orders; the result is the same.
	find "$1" -name '*.orig' -delete
}

fail=0
for p in "${patches[@]}"; do
	rm -rf "$tmp/alone"
	copy "$tmp/alone"
	apply "$tmp/alone" "$p" "alone" || fail=1
done

copy "$tmp/forward"
for p in "${patches[@]}"; do apply "$tmp/forward" "$p" "order" || fail=1; done
copy "$tmp/reverse"
for ((i = ${#patches[@]} - 1; i >= 0; i--)); do apply "$tmp/reverse" "${patches[i]}" "reverse order" || fail=1; done
if ! diff -r "$tmp/forward" "$tmp/reverse" > "$tmp/out"; then
	echo "FAILED: the patches give other files in reverse order"
	cat "$tmp/out"
	fail=1
fi

# The pristine tree with the patches is the whole of vendor/.
cp -R "$tmp/forward/." "$tmp/pristine/"
if ! diff -r "$tmp/pristine" vendor > "$tmp/out"; then
	echo "FAILED: vendor/ is not what the patches make of the module cache"
	head -60 "$tmp/out"
	fail=1
fi

if [ "$fail" = 0 ]; then
	echo "==> ${#patches[@]} patches, $(wc -l < "$tmp/files" | tr -d ' ') files: each applies alone, in either order the same, and vendor/ is the module cache with them."
fi
exit "$fail"
