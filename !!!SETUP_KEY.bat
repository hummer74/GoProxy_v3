@echo off
rem !!!SETUP_KEY.bat - выпуск нового SSH-ключа для GoProxy.
rem Использование: !!!SETUP_KEY.bat [имя_юзера] [--yes ^| -y]
rem   имя_юзера - латиница/цифры/дефис/подчёркивание, 1..32 символа, первый не дефис;
rem   --yes     - без вопросов (существующий ключ НЕ перезаписывается - отказ; мастер-ключ - оставить).
rem Требует прав администратора (сам перезапускается через UAC с передачей исходного юзера).
rem Вся работа внутри .ssh и GoProxy.ini рядом со скриптом.
setlocal DisableDelayedExpansion
cd /d "%~dp0"

set "AUTO_YES=0"
set "ORIGINAL_USER=%USERNAME%"
set "ARG_USER="

if /I "%~1"=="--elevated" goto :elevated_args
:parse_normal
if "%~1"=="" goto :parse_done
if /I "%~1"=="--yes" set "AUTO_YES=1"
if /I "%~1"=="-y" set "AUTO_YES=1"
if /I not "%~1"=="--yes" if /I not "%~1"=="-y" if not defined ARG_USER set "ARG_USER=%~1"
shift
goto :parse_normal
:parse_done
goto :after_args
:elevated_args
set "ORIGINAL_USER=%~2"
if /I "%~3"=="--yes" set "AUTO_YES=1"
if /I "%~3"=="-y" set "AUTO_YES=1"
if /I not "%~3"=="--yes" if /I not "%~3"=="-y" if not "%~3"=="" set "ARG_USER=%~3"
if /I "%~4"=="--yes" set "AUTO_YES=1"
if /I "%~4"=="-y" set "AUTO_YES=1"
:after_args
rem P0: владелец файлов - исходный юзер ДО elevation, а не Administrator.
set "CURRENT_USER=%ORIGINAL_USER%"

net session >nul 2>&1
if %errorLevel% == 0 (
    echo Administrator rights detected. File owner: %CURRENT_USER%
) else (
    echo Administrator rights are required to configure the service.
    echo Requesting elevation...
    set "FWD=--elevated \"%ORIGINAL_USER%\""
    if defined ARG_USER set "FWD=%FWD% \"%ARG_USER%\""
    if "%AUTO_YES%"=="1" set "FWD=%FWD% --yes"
    powershell -NoProfile -Command "Start-Process -FilePath '%~f0' -ArgumentList %FWD% -Verb RunAs"
    exit /b 0
)

:: Check if ssh-agent is running
sc query ssh-agent | find "RUNNING" >nul 2>&1
if %errorLevel% equ 0 (
    echo ssh-agent is running.
) else (
    echo ssh-agent is not running, starting...
    sc config ssh-agent start=auto >nul 2>&1
    sc start ssh-agent >nul 2>&1
    timeout /t 2 /nobreak > nul 2>&1
    sc query ssh-agent | find "RUNNING" >nul 2>&1
    if %errorLevel% neq 0 (
        echo ERROR: ssh-agent service failed to start.
        call :pause 5
        exit /b 1
    )
    echo ssh-agent started.
)
rem NB: ssh-add -D здесь убран сознательно - чужие ключи в агенте не трогаем.


cls
if defined ARG_USER (
    set "NEW_USER=%ARG_USER%"
    echo Using username from command line: %NEW_USER%
) else (
    set /p "NEW_USER=Enter username for new key: "
)
if not defined NEW_USER (
    echo Username cannot be empty.
    call :pause 5
    exit /b 1
)
rem P1: вайтлист - безопасно для имён файлов, ini и PowerShell. Проверка через env (без инъекций).
powershell -NoProfile -Command "if ($env:NEW_USER -notmatch '^[A-Za-z0-9][A-Za-z0-9_-]{0,31}$') { exit 1 }"
if errorlevel 1 (
    echo ERROR: bad username. Allowed: A-Z a-z 0-9 _ -, 1..32 chars, first char not - or _.
    call :pause 5
    exit /b 1
)

rem P2: идемпотентность - молча падать в вопрос ssh-keygen нельзя (виснет в --yes).
set "KEY_EXISTS=0"
if exist ".ssh\%NEW_USER%_key" set "KEY_EXISTS=1"
if exist ".ssh\%NEW_USER%_key.pub" set "KEY_EXISTS=1"
if exist ".ssh\%NEW_USER%_pass" set "KEY_EXISTS=1"
if "%KEY_EXISTS%"=="1" (
    if "%AUTO_YES%"=="1" (
        echo ERROR: key .ssh\%NEW_USER%_key already exists. Refusing silent overwrite. Delete it manually or run without --yes.
        exit /b 1
    )
    choice /C YN /N /M "Key .ssh\%NEW_USER%_key exists. Overwrite? (Y/N): "
    if errorlevel 2 (
        echo Aborted, existing key kept.
        exit /b 0
    )
    del /q ".ssh\%NEW_USER%_key" ".ssh\%NEW_USER%_key.pub" ".ssh\%NEW_USER%_pass" >nul 2>&1
)

rem P2: краткий лог операции рядом со скриптом (без пароля).
set "LOGFILE=%~dp0x_setup_%NEW_USER%.log"
echo [%DATE% %TIME%] setup started, user=%NEW_USER%, file-owner=%CURRENT_USER% > "%LOGFILE%"

pushd "%~dp0.ssh"
setlocal enabledelayedexpansion

:: Save full path to .ssh directory
set "SSH_DIR=%cd%"

rem P1: только ключевой материал (geoip/7z/pac не трогаем).
for %%F in (*_key *_key.pub *_pass *-key *-key.pub *-pass) do (
    if exist "%%F" (
        echo Processing file: "%%F"
        echo Processing file: "%%F" >> "!LOGFILE!"
        takeown /F "%%F" > nul 2>&1
        icacls "%%F" /reset > nul 2>&1
        icacls "%%F" /setowner "!CURRENT_USER!" > nul 2>&1
    )
)
echo Initial permissions reset done.


cls
echo Generating SSH key for %NEW_USER%...
<nul set /p="%NEW_USER%">"%NEW_USER%_pass"
ssh-keygen -t ed25519 -f "%NEW_USER%_key" -N "%NEW_USER%" -C "%NEW_USER%@%NEW_USER%" -q
if %errorLevel% neq 0 (
    echo ERROR: ssh-keygen failed.
    call :pause 5
    exit /b 1
)


cls
echo.
echo ============================================================
echo Key generated: %NEW_USER%_key
echo Passphrase file: %NEW_USER%_pass
echo ============================================================


cls
echo.
echo ============================================================
echo Setting strict file permissions...
echo ============================================================
for %%F in (*_key *_key.pub *_pass *-key *-key.pub *-pass) do (
    if exist "%%F" (
        echo Processing: %%F
        takeown /F "%%F" > nul 2>&1
        icacls "%%F" /reset > nul 2>&1
        icacls "%%F" /inheritance:r > nul 2>&1
        icacls "%%F" /setowner "!CURRENT_USER!" > nul 2>&1
        icacls "%%F" /grant:r "!CURRENT_USER!:F" > nul 2>&1
        icacls "%%F" /grant:r "*S-1-5-18:R" > nul 2>&1
    )
)
echo Done.


popd
echo Updating GoProxy.ini...
if exist GoProxy.ini (
    for /f "tokens=*" %%a in ('powershell -NoProfile -Command "Get-Date -Format 'yyyy-MMdd-HHmmss'"') do set "DT_INI=%%a"
    set "BACKUP_INI=GoProxy.ini.!DT_INI!"
    copy /Y GoProxy.ini "!BACKUP_INI!" >nul
    echo Backup saved as !BACKUP_INI!
    powershell -Command "(Get-Content GoProxy.ini) -replace '(?m)(?<=^\s*SSHKey\s*=\s*).*', ' .ssh\%NEW_USER%_key' -replace '(?m)(?<=^\s*SSHKeyPassword\s*=\s*).*', ' .ssh\%NEW_USER%_pass' | Set-Content GoProxy.ini"
) else (
    (
        echo [General]
        echo AppName              = GoProxy Manager
        echo LogSSHErrors         = false
        echo LogTunnelEvents      = true
        echo AutoConnect          = true
        echo SmartFailover        = true
        echo ReturnToOriginalHost = true
        echo FailoverResponseTime = 5
        echo HostsCheckInterval   = 180
        echo OriginalHostCheck    = 30
        echo.
        echo [Network]
        echo ProxyPort             = 1080
        echo PACHttpPort           = 8080
        echo InternetCheckDelay    = 5
        echo InternetCheckRetry    = 10
        echo SocksCheckInterval    = 10
        echo ReconnectAttemptDelay = 20
        echo MaxReconnectTime      = 7200
        echo InternetCheckURLs     = https://clients1.google.com/generate_204,http://connectivitycheck.gstatic.com/generate_204
        echo.
        echo [Paths]
        echo WorkDir        = .
        echo SSHConfig      = .ssh\config
        echo SSHKey         = .ssh\%NEW_USER%_key
        echo SSHKeyPassword = .ssh\%NEW_USER%_pass
        echo PACRules       = .ssh\proxy_pac.txt
        echo LastHostFile   = .ssh\x_lasthost.cfg
        echo.
        echo [TempFiles]
        echo SSHTunnelPID = x_ssh_tunnel.pid
        echo TrayPID      = x_tray_monitor.pid
        echo PACServerPID = x_http_pac.pid
        echo StateFile    = x_proxy_state.json
        echo StopFlag     = x_tray_stop_request.flag
        echo PACFile      = x_proxy.pac
    ) > GoProxy.ini
)
echo GoProxy.ini updated.


:: -----------------------------------------------------------------
:: 4) Updating SSH config (ROOT servers section only)
:: -----------------------------------------------------------------
pushd .ssh
echo Updating SSH config (ROOT servers)...

if exist config (
    for /f "tokens=*" %%a in ('powershell -NoProfile -Command "Get-Date -Format 'yyyy-MMdd-HHmmss'"') do set "DT=%%a"
    set "BACKUP=config.!DT!"
    copy /Y config "!BACKUP!" >nul
    echo Backup saved as !BACKUP!
)

set "PS_SCRIPT=%TEMP%\update_ssh_config_%RANDOM%.ps1"
(
echo $ErrorActionPreference = 'Stop'
echo $keyName = '%NEW_USER%_key'
echo $file = 'config'
echo $content = Get-Content $file -Raw
echo $lines = $content -split "`r`n"
echo $inRoot = $false
echo $output = @^(^)
echo $patched = 0
echo $totalHosts = 0
echo for ^($i = 0; $i -lt $lines.Count; $i++^) {
echo     $line = $lines[$i]
echo     # Detect start of ROOT servers section: #### / # ... ROOT servers ... / ####
echo      if ^(-not $inRoot -and $line -match '^#{3,}'^) {
echo         if ^($i+1 -lt $lines.Count -and $lines[$i+1] -match 'ROOT servers'^) {
echo              if ^($i+2 -lt $lines.Count -and $lines[$i+2] -match '^#{3,}'^) {
echo                  $inRoot = $true
echo                  $output += $line
echo                  $i++
echo                  $output += $lines[$i]
echo                  $i++
echo                  $output += $lines[$i]
echo                  continue
echo              }
echo          }
echo      }
echo      if ^($inRoot^) {
echo          # End of section
echo          if ^($line -match '^#{3,}'^) {
echo              $inRoot = $false
echo              $output += $line
echo              continue
echo          }
echo          # Process Host blocks - first token only, star skipped
echo          if ^($line -match '^\s*Host\s+^(\S+^)'^) {
echo              $hostName = $matches[1]
echo              if ^($hostName -ne '*'^) {
echo                  $totalHosts++
echo                  $output += $line
echo                  $i++
echo                  $blockLines = @^(^)
echo                  # stop at next Host or section boundary
echo                  while ^($i -lt $lines.Count -and $lines[$i] -notmatch '^\s*Host\s+' -and $lines[$i] -notmatch '^#{3,}'^) {
echo                      $blockLines += $lines[$i]
echo                      $i++
echo                  }
echo                  $hasKey = $false
echo                  foreach ^($bl in $blockLines^) {
echo                      if ^($bl -match ^('IdentityFile\s+' + [regex]::Escape^($keyName^)^)^) { $hasKey = $true; break }
echo                  }
echo                  if ^(-not $hasKey^) {
echo                      $foundKey = $false
echo                      $foundIdent = $false
echo                      $newBlock = @^(^)
echo                      foreach ^($bl in $blockLines^) {
echo                          if ^($bl -match '^\s*IdentityFile'^) {
echo                              if ^(-not $foundKey^) {
echo                                  $newBlock += ^('    IdentityFile ' + $keyName^)
echo                                  $foundKey = $true
echo                              }
echo                          } elseif ^($bl -match '^\s*IdentitiesOnly'^) {
echo                              $newBlock += ^('    IdentitiesOnly yes'^)
echo                              $foundIdent = $true
echo                          } else {
echo                              $newBlock += $bl
echo                          }
echo                      }
echo                      if ^(-not $foundKey^) {
echo                          $newBlock += ^('    IdentityFile ' + $keyName^)
echo                      }
echo                      if ^(-not $foundIdent^) {
echo                          $newBlock += ^('    IdentitiesOnly yes'^)
echo                      }
echo                      $output += $newBlock
echo                      $patched++
echo                  } else {
echo                      $output += $blockLines
echo                  }
echo                  $i--
echo                  continue
echo              }
echo          }
echo      }
echo      $output += $line
echo }
echo $newContent = $output -join "`r`n"
echo Set-Content -LiteralPath $file -Value $newContent -NoNewline
echo if ^($totalHosts -eq 0^) { Write-Error 'ROOT section: no host blocks found'; exit 1 }
echo Write-Host ^("patched $patched host block(s)"^)
) > "%PS_SCRIPT%"

if not exist "%PS_SCRIPT%" (
    echo ERROR: Failed to create PowerShell script.
    popd
    call :pause 15
    exit /b 1
)

powershell -NoProfile -ExecutionPolicy Bypass -File "%PS_SCRIPT%"
if errorlevel 1 (
    echo ERROR: PowerShell script failed.
    echo Restoring backup...
    if exist "!BACKUP!" (
        copy /Y "!BACKUP!" config >nul
    )
    del "%PS_SCRIPT%" 2>nul
    popd
    call :pause 15
    exit /b 1
)

del "%PS_SCRIPT%" 2>nul
rem P0: fail-closed - проверяем что патч реально лёг, иначе откат.
powershell -NoProfile -Command "exit ((Select-String -Path 'config' -Pattern ([regex]::Escape($env:NEW_USER + '_key')) | Measure-Object).Count -eq 0)"
if errorlevel 1 (
    echo ERROR: patched config has no %NEW_USER%_key references. Restoring backup...
    echo config patch had no effect, backup restored >> "!LOGFILE!"
    if exist "!BACKUP!" copy /Y "!BACKUP!" config >nul
    popd
    call :pause 15
    exit /b 1
)
rem P0: проверяем что ssh парсит итоговый конфиг (по первому хосту ROOT-секции).
set "FIRST_ROOT="
for /f "usebackq delims=" %%h in (`powershell -NoProfile -Command "$s=0; foreach ($l in Get-Content -LiteralPath 'config') { if ($s -eq 0) { if ($l -match 'ROOT servers') { $s=1 } continue }; if ($s -eq 1) { if ($l -match '^#{3,}') { $s=2 } continue }; if ($l -match '^#{3,}') { break }; if ($l -match '^\s*Host\s+(\S+)') { if ($Matches[1] -ne '*') { Write-Output $Matches[1]; break } } }"`) do set "FIRST_ROOT=%%h"
if not defined FIRST_ROOT (
    echo ERROR: no hosts in ROOT section. Restoring backup...
    echo no hosts in ROOT section, backup restored >> "!LOGFILE!"
    if exist "!BACKUP!" copy /Y "!BACKUP!" config >nul
    popd
    call :pause 15
    exit /b 1
)
ssh -F config -G "!FIRST_ROOT!" >nul 2>&1
if errorlevel 1 (
    echo ERROR: ssh rejects patched config. Restoring backup...
    echo ssh -G failed, backup restored >> "!LOGFILE!"
    copy /Y "!BACKUP!" config >nul
    popd
    call :pause 15
    exit /b 1
)
echo SSH config patched and verified.
echo SSH config patched and verified. >> "!LOGFILE!"


:: -----------------------------------------------------------------
:: 5) Load master key into ssh-agent
:: -----------------------------------------------------------------
echo.
echo Loading master key (opossum-key) into ssh-agent...

if not exist "opossum-pass" (
    if "%AUTO_YES%"=="1" (
        echo ERROR: opossum-pass not found in non-interactive mode.
        popd
        exit /b 1
    )
    echo No opossum-pass file - falling back to interactive ssh-add.
    ssh-add opossum-key
) else (
    set "TEMP_BAT=%TEMP%\ssh_askpass_%RANDOM%.bat"
    rem Пароль читаем и пишем без delayed expansion, иначе ! в пароле съедается.
    rem Askpass-обёртка не держит спецсимволы командной строки: такой пароль вводите вручную.
    setlocal DisableDelayedExpansion
    for /f "usebackq delims=" %%i in ("opossum-pass") do set "MASTER_PASS=%%i"
    (
        echo @echo off
        echo echo %MASTER_PASS%
    ) > "%TEMP_BAT%"
    endlocal
    set "SSH_ASKPASS=%TEMP_BAT%"
    set "DISPLAY=dummy:0"
    set "SSH_ASKPASS_REQUIRE=force"
    ssh-add opossum-key
    if defined TEMP_BAT del "%TEMP_BAT%" 2>nul
    set "TEMP_BAT="
    set "SSH_ASKPASS="
    set "DISPLAY="
    set "SSH_ASKPASS_REQUIRE="
)
echo Master key loaded.


:: -----------------------------------------------------------------
:: 6) Add public key to all hosts (via agent)
:: -----------------------------------------------------------------
echo.
echo Adding public key to all hosts...

set "HOST_LIST="
for /f "usebackq tokens=*" %%h in (`powershell -NoProfile -Command "Select-String -Path 'config' -Pattern '^\s*Host\s+([^\s*]+)' | ForEach-Object { $_.Matches.Groups[1].Value }"`) do (
    set "HOST_LIST=1"
    echo    Adding key to host: %%h
    rem -o IdentitiesOnly=no: разрешаем ключи агента, даже если конфиг их ограничивает
    type "%NEW_USER%_key.pub" | ssh -F config -o StrictHostKeyChecking=no -o IdentitiesOnly=no %%h "cat >> ~/.ssh/authorized_keys && chmod 600 ~/.ssh/authorized_keys"
    if errorlevel 1 (
        echo    WARNING: Failed to add key to %%h.
        echo    host %%h FAIL >> "!LOGFILE!"
    ) else (
        echo    OK.
        echo    host %%h OK >> "!LOGFILE!"
    )
)

if "!HOST_LIST!"=="" (
    echo WARNING: No hosts found in config.
    echo WARNING: no hosts found >> "!LOGFILE!"
)


:: -----------------------------------------------------------------
:: 7) Prompt to delete master key
:: -----------------------------------------------------------------
if exist "%NEW_USER%_key.pub" (
    if not exist opossum-key (
        echo ERROR: Master key 'opossum-key' not found.
        popd
        call :pause 15
        exit /b 1
    )
    echo *************************************************
    echo * All correct, keys added to all hosts.
    echo *
    echo *************************************************
    echo #       Do you want to delete master-key?
    echo #
    if "%AUTO_YES%"=="1" (
        echo --yes: master key kept.
        echo master key kept (--yes) >> "!LOGFILE!"
    ) else (
    choice /C YN /N /M "Enter Y or N: "
    if errorlevel 2 (
        echo *************************************************
        echo * ALARM! Master-key not deleted. ALARM!
        echo *************************************************
    ) else (
        del /s /q opossum-key opossum-pass >nul 2>&1
        echo *************************************************
        echo * Master-key is deleted. All correct!
        echo *************************************************
    )
    )
) else (
    echo ERROR: %NEW_USER%_key.pub not found.
    call :pause 15
    exit /b 1
)

popd

rem P1: аккуратная чистка - только наши ключи; сервис НЕ стопаем, он нужен GoProxy.
echo Cleaning up temporary agent identities...
if exist ".ssh\opossum-key" ssh-add -d ".ssh\opossum-key" >nul 2>&1
ssh-add -d ".ssh\%NEW_USER%_key" >nul 2>&1
echo Done. ssh-agent service left running.


cls
echo.
echo ============================================================
echo #             ALL OPERATION COMPLETE                        #
echo ============================================================
echo New key: .ssh\%NEW_USER%_key
echo Passphrase: %NEW_USER%
echo GoProxy.ini updated.
echo.
echo setup complete for %NEW_USER% >> "%LOGFILE%"
if defined BACKUP_INI echo ini backup: %BACKUP_INI% >> "%LOGFILE%"
if defined BACKUP echo config backup: %BACKUP% >> "%LOGFILE%"
echo Log: %LOGFILE%
call :pause 6
exit /b 0

rem P2: паузы только в интерактиве; в --yes сразу дальше.
:pause
if "%AUTO_YES%"=="1" exit /b 0
timeout /t %~1 /nobreak > nul 2>&1
exit /b 0
