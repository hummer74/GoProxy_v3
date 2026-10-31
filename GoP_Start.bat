@echo off
net session >nul 2>&1
if %errorLevel% == 0 (
    echo Administrator rights detected.
) else (
    echo Administrator rights are required to configure the service.
    echo Requesting elevation...
    powershell -Command "Start-Process '%~f0' -Verb RunAs"
    exit
)
echo Starting GoProxy...
"%~dp0GoProxy.exe"
echo GoProxy started.
timeout /t 2 /nobreak >nul
echo EXIT.
exit 0
