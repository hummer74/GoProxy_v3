@echo off
cd /d "%~dp0"
@echo on
go install github.com/akavel/rsrc@latest
rsrc -ico GoProxy.ico
go build -ldflags="-s -w -linkmode=external -extldflags=-static" -o GoProxy.exe .
