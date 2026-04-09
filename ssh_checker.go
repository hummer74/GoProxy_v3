package main

import (
        "bytes"
        "fmt"
        "os/exec"
        "strings"
        "time"

        windows "golang.org/x/sys/windows"
)

// Function hooks for easier testing
var checkSSHConnectionWithTimeFn = checkSSHConnectionWithTime

// checkSSHConnectionAdvanced проверяет SSH соединение с использованием той же команды, что и для туннеля
// Возвращает true если SSH команда успешно выполняется за FailoverResponseTime секунд
func checkSSHConnectionAdvanced(host HostConfig, workDir string) bool {
        debugLog("CHECKER", "Checking host: %s (%s:%s)", host.Name, host.HostName, host.Port)

        // Создаем тестовую SSH команду для проверки (без опций -N и -T)
        // Используем команду 'echo test', которая быстро завершится
        testCmd := buildTestSSHCommand(host, workDir)

        if len(testCmd) == 0 {
                logSSHError(host.Name, "INVALID_CONFIG", "Failed to build SSH test command")
                return false
        }

        cmd := exec.Command(testCmd[0], testCmd[1:]...)
        cmd.SysProcAttr = &windows.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}

        var stdout, stderr bytes.Buffer
        cmd.Stdout = &stdout
        cmd.Stderr = &stderr

        // Запускаем команду
        if err := cmd.Start(); err != nil {
                logSSHError(host.Name, "PROCESS_START", fmt.Sprintf("Failed to start SSH process: %v", err))
                return false
        }

        // Таймаут на основе конфигурации
        sshTimeout := time.Duration(Config.General.FailoverResponseTime) * time.Second
        if sshTimeout == 0 {
                sshTimeout = 5 * time.Second
        }

        done := make(chan error, 1)
        go func() {
                done <- cmd.Wait()
        }()

        select {
        case err := <-done:
                if err != nil {
                        // Команда завершилась с ошибкой
                        errorStr := strings.ToLower(stderr.String())

                        // Игнорируем некоторые ожидаемые ошибки
                        if strings.Contains(errorStr, "permission denied") ||
                                strings.Contains(errorStr, "authentication failed") ||
                                strings.Contains(errorStr, "publickey") ||
                                strings.Contains(errorStr, "key") {
                                // Аутентификация не удалась, но SSH сервер отвечает
                                return true
                        }

                        // Проверяем, может ли это быть ошибка ключа
                        if strings.Contains(errorStr, "private key") ||
                                strings.Contains(errorStr, "no mutual signature algorithm") {
                                return true
                        }

                        // Проверяем другие распространенные ошибки
                        if strings.Contains(errorStr, "connection refused") ||
                                strings.Contains(errorStr, "no route to host") ||
                                strings.Contains(errorStr, "network is unreachable") {
                                logSSHError(host.Name, "CONNECTION", errorStr)
                                return false
                        }

                        // Если ошибка неизвестна, считаем что сервер ответил
                        return true
                }

                // Команда завершилась успешно
                return true

        case <-time.After(sshTimeout):
                // Таймаут превышен
                if cmd.Process != nil {
                        killPid(cmd.Process.Pid)
                }
                logSSHError(host.Name, "TIMEOUT", fmt.Sprintf("%v timeout exceeded", sshTimeout))
                return false
        }
}

// checkSSHConnectionWithTime проверяет SSH соединение и возвращает время отклика
// Возвращает true и время отклика, если хост доступен
func checkSSHConnectionWithTime(host HostConfig, workDir string) (bool, time.Duration) {
        testCmd := buildTestSSHCommand(host, workDir)

        if len(testCmd) == 0 {
                logSSHError(host.Name, "INVALID_CONFIG", "Failed to build SSH test command")
                return false, 0
        }

        startTime := time.Now()

        cmd := exec.Command(testCmd[0], testCmd[1:]...)
        cmd.SysProcAttr = &windows.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}

        var stdout, stderr bytes.Buffer
        cmd.Stdout = &stdout
        cmd.Stderr = &stderr

        // Запускаем команду
        if err := cmd.Start(); err != nil {
                logSSHError(host.Name, "PROCESS_START", fmt.Sprintf("Failed to start SSH process: %v", err))
                return false, 0
        }

        // Таймаут на основе конфигурации (по умолчанию 5 секунд)
        timeout := time.Duration(Config.General.FailoverResponseTime) * time.Second
        if timeout == 0 {
                timeout = 5 * time.Second
        }

        done := make(chan error, 1)
        go func() {
                done <- cmd.Wait()
        }()

        select {
        case err := <-done:
                responseTime := time.Since(startTime)

                if err != nil {
                        // Команда завершилась с ошибкой
                        errorStr := strings.ToLower(stderr.String())

                        // Игнорируем некоторые ожидаемые ошибки
                        if strings.Contains(errorStr, "permission denied") ||
                                strings.Contains(errorStr, "authentication failed") ||
                                strings.Contains(errorStr, "publickey") ||
                                strings.Contains(errorStr, "key") {
                                // Аутентификация не удалась, но SSH сервер отвечает
                                return true, responseTime
                        }

                        // Проверяем, может ли это быть ошибка ключа
                        if strings.Contains(errorStr, "private key") ||
                                strings.Contains(errorStr, "no mutual signature algorithm") {
                                return true, responseTime
                        }

                        // Другие ошибки - хост недоступен
                        return false, 0
                }

                // Команда завершилась успешно
                return true, responseTime

        case <-time.After(timeout):
                // Таймаут
                if cmd.Process != nil {
                        killPid(cmd.Process.Pid)
                }
                logSSHError(host.Name, "TIMEOUT", fmt.Sprintf("%v timeout exceeded", timeout))
                return false, 0
        }
}

// buildTestSSHCommand строит тестовую SSH команду для проверки соединения
func buildTestSSHCommand(host HostConfig, workDir string) []string {
        cmd := []string{"ssh"}

        // Опции для проверки - такие же как в реальном подключении, но с более короткими таймаутами
        cmd = append(cmd,
                "-o", "BatchMode=yes", // Не запрашивать пароль
                "-o", fmt.Sprintf("ConnectTimeout=%d", Config.General.FailoverResponseTime), // Таймаут подключения из конфига
                "-o", "ServerAliveInterval=5", // Проверка активности
                "-o", "ServerAliveCountMax=6", // Быстро отключаться если нет ответа
                "-o", "StrictHostKeyChecking=accept-new",
                "-o", "UserKnownHostsFile=nul",
                "-o", "TCPKeepAlive=no",
                "-o", "LogLevel=ERROR", // Только ошибки
        )

        // Добавляем SSH-KEY если указан
        if host.IdentityFile != "" {
                sshKeyPath := resolveSSHKeyPath(workDir, host.IdentityFile)
                if sshKeyPath != "" {
                        cmd = append(cmd, "-i", sshKeyPath)
                }
        }

        // Порт
        if host.Port != "" && host.Port != "22" {
                cmd = append(cmd, "-p", host.Port)
        }

        // Пользователь
        user := host.User
        if user == "" {
                user = "root"
        }

        // Хост
        hostAddr := host.HostName
        if hostAddr == "" {
                return []string{} // Некорректный хост
        }

        // Формируем адрес подключения
        target := fmt.Sprintf("%s@%s", user, hostAddr)
        cmd = append(cmd, target)

        // Простая команда для проверки (быстро завершается)
        // Используем команду, которая работает на большинстве систем
        cmd = append(cmd, "exit 0")

        return cmd
}

// checkSSHConnectionBatch проверяет несколько хостов параллельно
func checkSSHConnectionBatch(hosts []HostConfig, workDir string) map[string]bool {
        debugLog("CHECKER", "Checking %d hosts (batch)", len(hosts))
        results := make(map[string]bool)

        // Если хостов немного, проверяем последовательно для лучшей диагностики
        if len(hosts) <= 3 {
                for _, host := range hosts {
                        results[host.Name] = checkSSHConnectionAdvanced(host, workDir)
                }
                return results
        }

        // Для большего количества хостов - параллельно
        resultChan := make(chan struct {
                name   string
                result bool
        }, len(hosts))

        // Максимум 3 параллельных проверки, чтобы не перегружать сеть
        semaphore := make(chan struct{}, 3)

        for _, host := range hosts {
                go func(h HostConfig) {
                        semaphore <- struct{}{}
                        defer func() { <-semaphore }()

                        result := checkSSHConnectionAdvanced(h, workDir)
                        resultChan <- struct {
                                name   string
                                result bool
                        }{h.Name, result}
                }(host)
        }

        // Собираем результаты
        for i := 0; i < len(hosts); i++ {
                res := <-resultChan
                results[res.name] = res.result
        }

        return results
}

// findFastestAvailableHost находит самый быстрый доступный хост из списка
func findFastestAvailableHost(hosts []HostConfig, workDir string) (*HostConfig, time.Duration) {
        debugLog("CHECKER", "Finding fastest among %d hosts", len(hosts))
        if len(hosts) == 0 {
                return nil, 0
        }

        type hostResult struct {
                host         HostConfig
                available    bool
                responseTime time.Duration
        }

        results := make(chan hostResult, len(hosts))
        semaphore := make(chan struct{}, 3) // Максимум 3 параллельных проверки

        for _, host := range hosts {
                go func(h HostConfig) {
                        semaphore <- struct{}{}
                        defer func() { <-semaphore }()

                        available, responseTime := checkSSHConnectionWithTimeFn(h, workDir)
                        results <- hostResult{
                                host:         h,
                                available:    available,
                                responseTime: responseTime,
                        }
                }(host)
        }

        // Собираем результаты
        var fastestHost *HostConfig
        var fastestTime time.Duration = 24 * time.Hour // Очень большое время

        for i := 0; i < len(hosts); i++ {
                result := <-results
                if result.available {
                        if result.responseTime < fastestTime {
                                fastestTime = result.responseTime
                                fastestHost = &result.host
                        }
                }
        }

        return fastestHost, fastestTime
}

// checkHostsAvailabilityWithTime проверяет доступность хостов с измерением времени
func checkHostsAvailabilityWithTime(hosts []HostConfig, workDir string) map[string]HostStatusWithTime {
        result := make(map[string]HostStatusWithTime)

        if len(hosts) == 0 {
                return result
        }

        type hostCheckResult struct {
                name   string
                status HostStatusWithTime
        }

        results := make(chan hostCheckResult, len(hosts))
        semaphore := make(chan struct{}, 3) // Максимум 3 параллельных проверки

        for _, host := range hosts {
                go func(h HostConfig) {
                        semaphore <- struct{}{}
                        defer func() { <-semaphore }()

                        available, responseTime := checkSSHConnectionWithTimeFn(h, workDir)

                        results <- hostCheckResult{
                                name: h.Name,
                                status: HostStatusWithTime{
                                        Host:         h,
                                        Available:    available,
                                        ResponseTime: responseTime,
                                        LastCheck:    time.Now(),
                                },
                        }
                }(host)
        }

        // Собираем результаты
        for i := 0; i < len(hosts); i++ {
                res := <-results
                result[res.name] = res.status
        }

        return result
}
