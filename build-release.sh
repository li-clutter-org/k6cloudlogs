#!/usr/bin/env bash

set -eEuo pipefail

eval "$(go env)"

# To override the latest git tag as the version, pass something else as the first arg.
VERSION=${1:-$(git describe --tags --always --dirty)}

# To overwrite the version details, pass something as the second arg. Empty string disables it.
VERSION_DETAILS=${2-"$(date -u +"%FT%T%z")/$(git describe --always --long --dirty)"}

make_archive() {
	local FMT="$1" DIR="$2"

	case $FMT in
	zip)
		zip -rq9 "$DIR.zip" "$DIR"
		;;
	tgz)
		tar -zcf "$DIR.tar.gz" "$DIR"
		;;
	esac
}

build_dist() {
	local ALIAS="$1" FMT="${2}" SUFFIX="${3}"  # Any other arguments are passed to the go build command as env vars
	local DIR="k6cloudlogs-${ALIAS}"

	local BUILD_ENV=("${@:4}")
	local BUILD_ARGS=(-o "dist/$DIR/k6cloudlogs${SUFFIX}" -trimpath)


	echo "- Building platform: ${ALIAS} (" "${BUILD_ENV[@]}" "go build" "${BUILD_ARGS[@]}" ")"

	# Clean out any old remnants of failed builds.
	rm -rf "dist/$DIR"
	mkdir -p "dist/$DIR"

	# Subshell to not mess with the current env vars or CWD
	(
		export "${BUILD_ENV[@]}"

		# Build a binary
	 	go build "${BUILD_ARGS[@]}"

		# Archive it all, native format depends on the platform.
		cd dist
		make_archive "$FMT" "$DIR"
	)

	# Delete the source files.
	rm -rf "dist/$DIR"
}

checksum() {
	local CHECKSUM_FILE="k6cloudlogs-checksums.txt"

	if command -v sha256sum > /dev/null; then
		CHECKSUM_CMD=("sha256sum")
	elif command -v shasum > /dev/null; then
		CHECKSUM_CMD=("shasum" "-a" "256")
	else
		echo "ERROR: unable to find a command to compute sha-256 hash"
		return 1
	fi

	rm -f "dist/$CHECKSUM_FILE"
	( cd dist && for x in *; do [ -f "$x" ] && "${CHECKSUM_CMD[@]}" -- "$x" >> "$CHECKSUM_FILE"; done )
}

echo "--- Building Release: ${VERSION}"

echo "-> Building platform packages..."
mkdir -p dist

build_dist mac     zip ""   GOOS=darwin  GOARCH=amd64  CGO_ENABLED=0
build_dist win64   zip .exe GOOS=windows GOARCH=amd64  CGO_ENABLED=0
build_dist linux64 tgz ""   GOOS=linux   GOARCH=amd64  CGO_ENABLED=0

echo "-> Generating checksum file..."
checksum
