# GoProxy_v5 — рабочие соглашения

Windows-only трей-менеджер SSH SOCKS-прокси (Go, systray).

- Сборка: `!!!BUILD.bat` (`go build -ldflags=-s -w -linkmode=external -extldflags=-static` + `upx --ultra-brute` → ~2,6 МБ).
- Деплой = замена одного `goproxy.exe` (self-contained).
- `.ssh/` (ключи, `config`) и `*.ini` — машинно-локальны, в `.gitignore`, не коммитятся.
- Конфиг `GoProxy.ini` обратно-совместим: новые поля подставляют дефолты через `setDefaultConfig`.
