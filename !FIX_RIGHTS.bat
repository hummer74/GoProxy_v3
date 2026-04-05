@echo off

cd /d "%~dp0"

net session >nul 2>&1
if %errorLevel% == 0 (
    echo Administrator rights detected.
) else (
    echo Administrator rights are required to configure the service.
    echo Requesting elevation...
    powershell -Command "Start-Process '%~f0' -Verb RunAs"
    exit
)

:: Setup ssh-agent as Windows-daemon
sc config ssh-agent start=auto
sc start ssh-agent 

cls
pushd "%~dp0.ssh"
echo.
echo '''''''''''''''''''''''''''''''''''''''''''''''''
echo *************************************************
echo # Note: The folder permissions remain UNTOUCHED #
echo #     SSH File Security Setup (Files ONLY).     #
echo *************************************************
echo .................................................
echo.
echo.
:CHOICE
echo #   Do you want (S)set privileges or (R)eset ?   #
set /p "answer=Enter (s) or (r) for the choice: "

if /I "%answer%"=="s" (goto SET) else if /I "%answer%"=="r" (goto RESET) else (
    echo Invalid choice. Please enter 's' to set or 'r' to reset.
    goto CHOICE
)

:SET
setlocal enabledelayedexpansion
echo.
echo === Reset + Set ===
for %%F in (*) do (
    echo - %%F
    takeown /F "%%F" > nul 2>&1
    icacls "%%F" /reset > nul 2>&1
    icacls "%%F" /inheritance:r > nul 2>&1
    icacls "%%F" /setowner "!USERNAME!" > nul 2>&1
    icacls "%%F" /grant:r "!USERNAME!:F" > nul 2>&1
    icacls "%%F" /grant:r "*S-1-5-18:R" > nul 2>&1
)
endlocal
goto FINAL

:RESET
setlocal enabledelayedexpansion
echo.
echo === Reset ===
for %%F in (*) do (
    echo - %%F
    takeown /F "%%F" > nul 2>&1
    icacls "%%F" /reset > nul 2>&1
    icacls "%%F" /setowner "!USERNAME!" > nul 2>&1
)
endlocal
goto FINAL


:FINAL

popd

echo.
echo --------------------------------------------
echo === Finished ===
timeout /t 3 /nobreak > nul 2>&1
exit
