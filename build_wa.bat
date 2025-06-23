echo "build Windows x86"
move "%~dp0main.go" "%~dp0main.go.bak"
move "%~dp0main.go.windows" "%~dp0main.go"
gcc -m32 "%~dp0.dora\dora.c" -o Dora.exe -Wl,Dora.def
dlltool -d Dora.def -e Dora.exp -l libdora.a -m i386 --dllname Dora.exe
set GOPROXY=https://goproxy.cn,direct
set GOOS=windows
set GOARCH=386
set CGO_ENABLED=1
set GOFLAGS=-buildvcs=false
go build -buildmode=c-shared -ldflags="-s -w" -o wa.dll
lib /def:wa.def /name:wa.dll /out:wa.lib /MACHINE:X86
xcopy wa.lib "%~dp0..\Dora-SSR\Source\3rdParty\Wa\Lib\Windows\wa.lib" /Y
xcopy wa.dll "%~dp0..\Dora-SSR\Source\3rdParty\Wa\Lib\Windows\wa.dll" /Y
move "%~dp0main.go" "%~dp0main.go.windows"
move "%~dp0main.go.bak" "%~dp0main.go"
del /q Dora.exe
del /q Dora.exp
del /q libdora.a
del /q wa.lib
del /q wa.exp
del /q wa.dll
del /q wa.h
