#!/usr/bin/env bash
set -e

cd "$(dirname "$0")"
echo "build Linux amd64"
GOPROXY=https://goproxy.cn,direct GOOS=linux GOARCH=amd64 CGO_ENABLED=1 \
go build -buildmode=c-archive -ldflags="-s -w" -o libwa.a
cp libwa.a "$(dirname "$0")/../Dora-SSR/Source/3rdParty/Wa/Lib/Linux/amd64/"
rm -f libwa.a libwa.h
