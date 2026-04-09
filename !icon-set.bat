@echo off
cd /d "%~dp0"
@echo on
go install github.com/akavel/rsrc@latest
rsrc -ico GoProxy.ico
go build -ldflags="-s -w -linkmode=external -extldflags=-static" -o GoProxy.exe .

@echo off
if %ERRORLEVEL% NEQ 0 (
    echo [ERROR] Build failed, skipping UPX.
    pause
    exit /b %ERRORLEVEL%
)
timeout /t 5 /nobreak >nul
exit 0
upx --ultra-brute GoProxy.exe
timeout /t 1 /nobreak >nul
exit 0