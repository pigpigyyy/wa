echo "build Windows x86"
set GOOS=windows
set GOARCH=386
set CGO_ENABLED=1
set CC=zig cc -target x86-windows-msvc
set GOFLAGS=-buildvcs=false
go build -buildmode=c-archive -ldflags="-s -w" -o wa.lib
copy wa.lib "%~dp0..\Dora-SSR\Source\3rdParty\Wa\Lib\Windows\wa.lib"