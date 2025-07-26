echo "build Windows x86"
set GOOS=windows
set GOARCH=386
set CGO_ENABLED=1
set GOFLAGS=-buildvcs=false
go build -buildmode=c-shared -ldflags="-s -w" -o wa.dll
lib /def:wa.def /name:wa.dll /out:wa.lib /MACHINE:X86
xcopy wa.lib "%~dp0..\Dora-SSR\Source\3rdParty\Wa\Lib\Windows\wa.lib" /Y
xcopy wa.dll "%~dp0..\Dora-SSR\Source\3rdParty\Wa\Lib\Windows\wa.dll" /Y
del /q wa.lib
del /q wa.exp
del /q wa.dll
del /q wa.h
