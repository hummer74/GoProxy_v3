@echo off
net session >nul 2>&1
if %errorLevel% == 0 (
    echo Administrator rights detected. >> "%~dp0x_gopstart-cli.log" 2>&1
) else (
    echo Administrator rights are required to configure the service. >> "%~dp0x_gopstart-cli.log" 2>&1
    echo Requesting elevation... >> "%~dp0x_gopstart-cli.log" 2>&1
    powershell -Command "Start-Process '%~f0' -Verb RunAs" >> "%~dp0x_gopstart-cli.log" 2>&1
    exit
)
echo Stopping GoProxy... >> "%~dp0x_gopstart-cli.log" 2>&1
"%~dp0GoProxy.exe" -stop >> "%~dp0x_gopstart-cli.log" 2>&1
timeout /t 1 /nobreak >nul
ssh-add -d
timeout /t 1 /nobreak >nul
taskkill /F /IM ssh-agent.exe /IM ssh-add.exe >> "%~dp0x_gopstart-cli.log" 2>&1
timeout /t 1 /nobreak >nul
echo Starting GoProxy... >> "%~dp0x_gopstart-cli.log" 2>&1
"%~dp0GoProxy.exe" -cli >> "%~dp0x_gopstart-cli.log" 2>&1
echo GoProxy started. >> "%~dp0x_gopstart-cli.log" 2>&1
timeout /t 2 /nobreak >nul
echo EXIT. >> "%~dp0x_gopstart-cli.log" 2>&1
exit 0