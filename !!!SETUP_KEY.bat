@echo off
cd /d "%~dp0"
set "CURRENT_USER=%USERNAME%"

net session >nul 2>&1
if %errorLevel% == 0 (
    echo Administrator rights detected.
) else (
    echo Administrator rights are required to configure the service.
    echo Requesting elevation...
    powershell -Command "Start-Process '%~f0' -Verb RunAs"
    exit
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
        timeout /t 5 /nobreak > nul 2>&1
        exit /b 1
    )
    echo ssh-agent started.
)
:: Remove all keys from agent
ssh-add -D >nul 2>&1
echo All keys removed from ssh-agent.
timeout /t 3 /nobreak > nul 2>&1

cls
set /p "NEW_USER=Enter username for new key: "
if "%NEW_USER%"=="" (
    echo Username cannot be empty.
    timeout /t 5 /nobreak > nul 2>&1
    exit /b 1
)

pushd "%~dp0.ssh"
setlocal enabledelayedexpansion

:: Save full path to .ssh directory
set "SSH_DIR=%cd%"

:: Initial processing of existing files
for %%F in (*) do (
    echo --------------------------------------------
    echo Processing file: "%%F"
    takeown /F "%%F" > nul 2>&1 
    icacls "%%F" /reset > nul 2>&1 
    icacls "%%F" /setowner "!CURRENT_USER!" > nul 2>&1 
    echo Done.
)
echo Initial permissions reset done.
timeout /t 3 /nobreak > nul 2>&1

cls
echo Generating SSH key for %NEW_USER%...
<nul set /p="%NEW_USER%">"%NEW_USER%_pass"
ssh-keygen -t ed25519 -f "%NEW_USER%_key" -N "%NEW_USER%" -C "%NEW_USER%@%NEW_USER%" -q
if %errorLevel% neq 0 (
    echo ERROR: ssh-keygen failed.
    timeout /t 5 /nobreak > nul 2>&1
    exit /b 1
)
timeout /t 3 /nobreak > nul 2>&1

cls
echo.
echo ============================================================
echo Key generated: %NEW_USER%_key
echo Passphrase file: %NEW_USER%_pass
echo ============================================================
timeout /t 3 /nobreak > nul 2>&1

cls
echo.
echo ============================================================
echo Setting strict file permissions...
echo ============================================================
for %%F in (*) do (
    echo Processing: %%F
    takeown /F "%%F" > nul 2>&1
    icacls "%%F" /reset > nul 2>&1
    icacls "%%F" /inheritance:r > nul 2>&1
    icacls "%%F" /setowner "!CURRENT_USER!" > nul 2>&1
    icacls "%%F" /grant:r "!CURRENT_USER!:F" > nul 2>&1
    icacls "%%F" /grant:r "*S-1-5-18:R" > nul 2>&1
)
echo Done.
timeout /t 3 /nobreak > nul 2>&1

popd
echo Updating GoProxy.ini...
if exist GoProxy.ini (
    for /f "tokens=*" %%a in ('powershell -NoProfile -Command "Get-Date -Format 'yyyy-MMdd-HHmmss'"') do set "DT_INI=%%a"
    set "BACKUP_INI=GoProxy.ini.!DT_INI!"
    copy /Y GoProxy.ini "!BACKUP_INI!" >nul
    echo Backup saved as !BACKUP_INI!
    powershell -Command "(Get-Content GoProxy.ini) -replace '(?<=SSHKey\s*=\s*).*', ' .ssh\%NEW_USER%_key' -replace '(?<=SSHKeyPassword\s*=\s*).*', ' .ssh\%NEW_USER%_pass' | Set-Content GoProxy.ini"
) else (
    (
        echo [General]
        echo AppName              = GoProxy Manager
        echo LogSSHErrors         = false
        echo LogTunnelEvents      = true
        echo AutoConnect          = true
        echo SmartFailover        = true
        echo ReturnToOriginalHost = true
        echo AutoSelectTimeout    = 3
        echo FailoverResponseTime = 5
        echo HostsCheckInterval   = 180
        echo OriginalHostCheck    = 60
        echo.
        echo [Network]
        echo ProxyPort             = 1080
        echo PACHttpPort           = 8080
        echo InternetCheckDelay    = 5
        echo InternetCheckRetry    = 10
        echo SocksCheckInterval    = 30
        echo ReconnectAttemptDelay = 20
        echo MaxReconnectTime      = 7200
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
timeout /t 3 /nobreak > nul 2>&1

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
echo for ^($i = 0; $i -lt $lines.Count; $i++^) {
echo     $line = $lines[$i]
echo      # Detect start of ROOT servers section
echo      if ^(-not $inRoot -and $line -match '^#{3,}'^) {
echo          if ^($i+1 -lt $lines.Count -and $lines[$i+1] -eq '# ROOT servers'^) {
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
echo          # Process Host blocks
echo          if ^($line -match '^\s*Host\s+^(\S+^)'^) {
echo              $hostName = $matches[1]
echo              if ^($hostName -ne '*'^) {
echo                  $output += $line
echo                  $i++
echo                  $blockLines = @^(^)
echo                  while ^($i -lt $lines.Count -and $lines[$i] -notmatch '^\s*Host\s+'^) {
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
echo Set-Content $file -Value $newContent -NoNewline
echo Write-Host 'SSH config updated.'
) > "%PS_SCRIPT%"

if not exist "%PS_SCRIPT%" (
    echo ERROR: Failed to create PowerShell script.
    popd
    timeout /t 15 /nobreak > nul 2>&1
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
    timeout /t 15 /nobreak > nul 2>&1
    exit /b 1
)

del "%PS_SCRIPT%" 2>nul
echo SSH config updated successfully.
timeout /t 3 /nobreak > nul 2>&1

:: -----------------------------------------------------------------
:: 5) Load master key into ssh-agent
:: -----------------------------------------------------------------
echo.
echo Loading master key (opossum-key) into ssh-agent...

if not exist "opossum-pass" (
    ssh-add opossum-key 2>nul
) else (
    set "MASTER_PASS="
    for /f "usebackq delims=" %%i in ("opossum-pass") do set "MASTER_PASS=%%i"
    set "TEMP_BAT=%TEMP%\ssh_askpass_%RANDOM%.bat"
    (
        echo @echo off
        echo echo !MASTER_PASS!
    ) > "!TEMP_BAT!"
    set "SSH_ASKPASS=!TEMP_BAT!"
    set "DISPLAY=dummy:0"
    set "SSH_ASKPASS_REQUIRE=force"
    ssh-add opossum-key
    del "!TEMP_BAT!" 2>nul
    set "SSH_ASKPASS="
    set "DISPLAY="
    set "SSH_ASKPASS_REQUIRE="
)
echo Master key loaded.
timeout /t 3 /nobreak > nul 2>&1

:: -----------------------------------------------------------------
:: 6) Add public key to all hosts (via agent)
:: -----------------------------------------------------------------
echo.
echo Adding public key to all hosts...

set "HOST_LIST="
for /f "usebackq tokens=*" %%h in (`powershell -NoProfile -Command "Select-String -Path 'config' -Pattern '^\s*Host\s+([^\s*]+)' | ForEach-Object { $_.Matches.Groups[1].Value }"`) do (
    set "HOST_LIST=1"
    echo    Adding key to host: %%h
    :: ADDED: -o IdentitiesOnly=no to allow use of ssh-agent keys even if config restricts them
    type "%NEW_USER%_key.pub" | ssh -F config -o StrictHostKeyChecking=no -o IdentitiesOnly=no %%h "cat >> ~/.ssh/authorized_keys && chmod 600 ~/.ssh/authorized_keys"
    if errorlevel 1 (
        echo    WARNING: Failed to add key to %%h.
    ) else (
        echo    OK.
    )
)

if "!HOST_LIST!"=="" (
    echo WARNING: No hosts found in config.
)
timeout /t 3 /nobreak > nul 2>&1

:: -----------------------------------------------------------------
:: 7) Prompt to delete master key
:: -----------------------------------------------------------------
if exist "%NEW_USER%_key.pub" (
    if not exist opossum-key (
        echo ERROR: Master key 'opossum-key' not found.
        popd
        timeout /t 15 /nobreak > nul 2>&1
        exit /b 1
    )
    echo *************************************************
    echo * All correct, keys added to all hosts.
    echo *
    echo *************************************************
    echo #       Do you want to delete master-key?
    echo #
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
    timeout /t 10 /nobreak > nul 2>&1
) else (
    echo ERROR: %NEW_USER%_key.pub not found.
    timeout /t 15 /nobreak > nul 2>&1
    exit /b 1
)

popd

:: Clean up ssh-agent before exit
echo Cleaning up ssh-agent...
ssh-add -D >nul 2>&1
echo All keys removed from ssh-agent.
sc stop ssh-agent >nul 2>&1
echo ssh-agent stopped.
timeout /t 3 /nobreak > nul 2>&1

cls
echo.
echo ============================================================
echo #             ALL OPERATION COMPLETE                        #
echo ============================================================
echo New key: .ssh\%NEW_USER%_key
echo Passphrase: %NEW_USER%
echo GoProxy.ini updated.
echo.
timeout /t 6 /nobreak > nul 2>&1
exit /b 0
