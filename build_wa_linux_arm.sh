cd "$(dirname "$0")"
echo "build Linux arm64"
GOPROXY=https://goproxy.cn,direct GOOS=linux GOARCH=arm64 CGO_ENABLED=1 \
go build -buildmode=c-archive -ldflags="-s -w" -o libwa.a
cp libwa.a "$(dirname "$0")/../Dora-SSR/Source/3rdParty/Wa/Lib/Linux/aarch64/"
rm -f libwa.a libwa.h

