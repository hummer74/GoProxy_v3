// z_process_guard_test.go — тесты защиты от убийства чужих процессов.
//
// killProcessByFile / killStaleListeners обязаны сверять владельца
// (PID может быть переиспользован, порт — занят чужим софтом).
package main

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// processImageName / verifyPidImage
// ---------------------------------------------------------------------------

func TestProcessImageName_Self(t *testing.T) {
	img, ok := processImageName(os.Getpid())
	if !ok || img == "" {
		t.Fatalf("processImageName(self) = %q, %v — want own image", img, ok)
	}
	want := ownExeName()
	if img != want && !strings.EqualFold(img, want) {
		t.Errorf("processImageName(self) = %q, want %q", img, want)
	}
}

func TestVerifyPidImage(t *testing.T) {
	own := ownExeName()
	if !verifyPidImage(os.Getpid(), own) {
		t.Errorf("verifyPidImage(self, %q) = false, want true", own)
	}
	if verifyPidImage(os.Getpid(), "definitely-not-ours.exe") {
		t.Error("verifyPidImage(self, foreign) = true, want false")
	}
	// Несуществующий PID: tasklist его не найдёт.
	if verifyPidImage(2147483647, "ssh.exe") {
		t.Error("verifyPidImage(dead PID) = true, want false")
	}
}

func TestOwnExeName_NonEmpty(t *testing.T) {
	if ownExeName() == "" {
		t.Error("ownExeName() must not be empty")
	}
}

func TestExpectedImageFor(t *testing.T) {
	if expectedImageFor("SSH Tunnel") != "ssh.exe" {
		t.Errorf("SSH Tunnel must map to ssh.exe, got %q", expectedImageFor("SSH Tunnel"))
	}
	if expectedImageFor("SSH Tunnel (stale)") != "ssh.exe" {
		t.Errorf("stale SSH Tunnel must map to ssh.exe, got %q", expectedImageFor("SSH Tunnel (stale)"))
	}
	if expectedImageFor("Tray") == "ssh.exe" || expectedImageFor("PAC HTTP Server") == "ssh.exe" {
		t.Error("tray/PAC entries must map to our own exe, not ssh.exe")
	}
}

// ---------------------------------------------------------------------------
// killProcessByFile: чужой PID в файле — не убивать, файл удалить.
// ---------------------------------------------------------------------------

func TestKillProcessByFile_ForeignPID(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "x_foreign.pid")
	// Пишем СВОЙ pid, но с чужим Info: verify обязан отказать.
	data, _ := json.Marshal(PidData{Pid: os.Getpid(), Info: "SSH Tunnel"})
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		t.Fatal(err)
	}
	// Если гард сломан, этот вызов убьёт тестовый процесс.
	killProcessByFile(tmp, "SSH Tunnel")
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Error("stale PID file must be removed even when the process is spared")
	}
	// Дожили до сюда — процесс не убит. Явная проверка живости:
	if _, ok := processImageName(os.Getpid()); !ok {
		t.Error("test process is gone — the guard killed our own PID")
	}
}

func TestKillProcessByFile_SystemPID(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "x_sys.pid")
	data, _ := json.Marshal(PidData{Pid: 4, Info: "SSH Tunnel"})
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		t.Fatal(err)
	}
	killProcessByFile(tmp, "SSH Tunnel")
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Error("PID file with system PID must be removed without killing")
	}
}

// ---------------------------------------------------------------------------
// killStaleListeners: свободный порт — no-op, без зависаний.
// ---------------------------------------------------------------------------

func TestKillStaleListeners_FreePort(t *testing.T) {
	// Порт 0 заведомо не LISTENING — функция должна просто вернуться.
	killStaleListeners(0)
}

func TestWaitForPACServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	if !waitForPACServer(port, 3*time.Second) {
		t.Errorf("waitForPACServer(live :%d) = false, want true", port)
	}
	// Порт 0 заведомо не слушается: каждый dial падает сразу,
	// но функция честно ждёт весь таймаут и возвращает false.
	if waitForPACServer(0, 3*time.Second) {
		t.Error("waitForPACServer(:0) = true, want false")
	}
}
