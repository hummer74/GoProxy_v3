@echo off

:start

cls
echo `
echo * * * * * * * * * *
echo * HTTP PAC PROXY  *
netstat -aon | findstr "127.0.0.1:8080" | findstr LISTENING
echo `
echo * * * * * * * * * *
echo * SOCKS5  PROXY   *
netstat -ano | findstr /R "127.0.0.1.*127.0.0.1:1080" | findstr ESTABLISHED
echo * * * * * * * * * *

timeout /t 1 /nobreak > nul 2>&1 
cls
goto :start