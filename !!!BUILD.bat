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

:: Ожидание 10 секунд. При нажатии любой клавиши выходим с кодом 0, иначе продолжаем UPX
echo Building complete. Exit (Y) or packing (N) ?
choice /t 10 /c yn /d n > nul
if errorlevel 2 goto :run_upx
exit /b 0

:run_upx
upx --ultra-brute GoProxy.exe
timeout /t 1 /nobreak > nul
exit 0