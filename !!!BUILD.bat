@echo off
cd /d "%~dp0"
@echo on
go build -ldflags="-s -w -linkmode=external -extldflags=-static" -o GoProxy.exe .

@echo off
if %ERRORLEVEL% NEQ 0 (
    echo [ERROR] Build failed, skipping UPX.
    pause
    exit /b %ERRORLEVEL%
)
upx --ultra-brute GoProxy.exe
timeout /t 1 /nobreak >nul
exit 0