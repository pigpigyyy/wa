@echo off
setlocal

cd /d "%~dp0"

echo build Windows x86
set GOOS=windows
set GOARCH=386
set CGO_ENABLED=1
set GOFLAGS=-buildvcs=false

go build -buildmode=c-shared -ldflags="-s -w" -o wa.dll
if errorlevel 1 exit /b 1

xcopy wa.dll "%~dp0..\Dora-SSR\Source\3rdParty\Wa\Lib\Windows\wa.dll" /Y
if errorlevel 1 exit /b 1

del /q wa.dll 2>nul
del /q wa.h 2>nul

endlocal
