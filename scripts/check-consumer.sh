#!/bin/sh
# Compile and run a separate consumer module using only public edge imports.
set -eu
repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
consumer_dir=$(mktemp -d)
trap 'rm -rf "$consumer_dir"' EXIT HUP INT TERM
cp "$repo_root/testdata/consumer/consumer_test.go.txt" "$consumer_dir/consumer_test.go"
cd "$consumer_dir"
go mod init example.com/edge-consumer
go mod edit -go=1.27.0 -require=github.com/asteroid-computing/go-lambda-edge@v0.0.0
go mod edit "-replace=github.com/asteroid-computing/go-lambda-edge=$repo_root"
go test -mod=mod .
