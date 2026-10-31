package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestTrySSHAddCleansUpBatchFile verifies that trySSHAdd does not leave
// ssh_pass_*.bat files in the system temp directory.
func TestTrySSHAddCleansUpBatchFile(t *testing.T) {
	// Check if ssh-add is available in PATH; if not, we'll create a fake one.
	if _, err := exec.LookPath("ssh-add"); err != nil {
		t.Skip("ssh-add not found, skipping test")
	}

	// Save original PATH to restore later.
	origPath := os.Getenv("PATH")
	defer func() { os.Setenv("PATH", origPath) }()

	// Create a temporary directory to hold our fake ssh-add.
	fakeDir, err := os.MkdirTemp("", "sshadd_fake_*")
	if err != nil {
		t.Fatalf("failed to create temp dir for fake ssh-add: %v", err)
	}
	defer os.RemoveAll(fakeDir)

	// Create a fake ssh-add script that just exits successfully.
	// On Windows, we can create a .bat file.
	fakeSSHAdd := filepath.Join(fakeDir, "ssh-add.bat")
	if err := os.WriteFile(fakeSSHAdd, []byte("@echo off\r\nexit /b 0\r\n"), 0755); err != nil {
		t.Fatalf("failed to write fake ssh-add: %v", err)
	}

	// Prepend our fake directory to PATH so that exec.LookPath will find our fake first.
	if err := os.Setenv("PATH", fakeDir+string(filepath.ListSeparator)+origPath); err != nil {
		t.Fatalf("failed to set PATH: %v", err)
	}

	// Now ssh-add should resolve to our fake.
	if _, err := exec.LookPath("ssh-add"); err != nil {
		t.Fatalf("failed to find fake ssh-add in PATH: %v", err)
	}

	// Create a temporary SSH key file (content doesn't matter).
	keyFile, err := os.CreateTemp("", "id_test_*")
	if err != nil {
		t.Fatalf("failed to create temp key file: %v", err)
	}
	keyPath := keyFile.Name()
	defer os.Remove(keyPath)

	// Call trySSHAdd with a dummy passphrase.
	// We don't care about the return value; we only care about cleanup.
	_ = trySSHAdd(keyPath, "secret")

	// Check that no ssh_pass_*.bat files remain in the system temp directory.
	tmpDir := os.TempDir()
	files, err := filepath.Glob(filepath.Join(tmpDir, "ssh_pass_*.bat"))
	if err != nil {
		t.Fatalf("failed to glob ssh_pass_*.bat files: %v", err)
	}
	if len(files) > 0 {
		t.Errorf("found %d ssh_pass_*.bat files left in temp dir: %v", len(files), files)
	}
}
