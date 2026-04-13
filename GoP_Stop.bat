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
echo Stopping GoProxy...
"%~dp0GoProxy.exe" -stop
timeout /t 2 /nobreak >nul
ssh-add -d
timeout /t 1 /nobreak >nul
taskkill /F /IM ssh-agent.exe /IM ssh-add.exe
timeout /t 1 /nobreak >nul
del "%~dp0.ssh\goproxy_known_hosts" /q
echo EXIT.
exit 0