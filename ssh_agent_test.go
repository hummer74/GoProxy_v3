package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestAddKeyToAgentCleansUpAskPass ensures that no askpass_*.bat files are left
// in the work directory after addKeyToAgent returns (for both empty and non-empty passphrase).
func TestAddKeyToAgentCleansUpAskPass(t *testing.T) {
	// Check if ssh-add is available in PATH.
	if _, err := exec.LookPath("ssh-add"); err != nil {
		t.Skip("ssh-add not found, skipping test")
	}

	// Save original config and functions to restore later.
	origConfig := Config
	origCheckAndStartSSHAgent := checkAndStartSSHAgent
	origIsKeyInAgent := isKeyInAgent
	origLoadSSHKeyPassphrase := loadSSHKeyPassphrase
	defer func() {
		Config = origConfig
		checkAndStartSSHAgent = origCheckAndStartSSHAgent
		isKeyInAgent = origIsKeyInAgent
		loadSSHKeyPassphrase = origLoadSSHKeyPassphrase
	}()

	// Use a temporary directory as work dir to avoid polluting the real one.
	tmpDir, err := os.MkdirTemp("", "goproxy_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create a dummy SSH key file (empty, just for existence).
	keyFile, err := os.CreateTemp(tmpDir, "id_*.key")
	if err != nil {
		t.Fatalf("failed to create temp key file: %v", err)
	}
	keyPath := keyFile.Name()
	// We just need the file to exist; content doesn't matter for this test.
	// Ensure it exists (CreateTemp already created it).
	defer os.Remove(keyPath) // clean up key file after test

	// Set Config to use our temporary directory.
	Config = &AppConfig{}
	setDefaultConfig() // load defaults
	// Override paths to use tmpDir.
	Config.Paths.WorkDir = tmpDir
	Config.Paths.SSHKey = filepath.Join(tmpDir, "id_key") // not used but set.
	resolvePaths() // adjust absolute paths.

	// Mock checkAndStartSSHAgent to return true (assume agent is running).
	checkAndStartSSHAgent = func() bool { return true }
	// Mock isKeyInAgent to return false so we go through the add path.
	isKeyInAgent = func(string) bool { return false }
	// We'll test with empty passphrase first, then non-empty.

	for _, passphrase := range []string{"", "secret123"} {
		loadSSHKeyPassphrase = func() string { return passphrase }

		// Call addKeyToAgent.
		ok := addKeyToAgent(keyPath, passphrase)
		// We don't care about the return value for this test; we only care about cleanup.
		// But we log it for debugging.
		t.Logf("addKeyToAgent with passphrase %q returned %v", passphrase, ok)

		// Check that no askpass_*.bat files remain in tmpDir.
		files, err := filepath.Glob(filepath.Join(tmpDir, "askpass_*.bat"))
		if err != nil {
			t.Fatalf("failed to glob askpass files: %v", err)
		}
		if len(files) > 0 {
			t.Errorf("found %d askpass_*.bat files left in workdir after passphrase %q: %v", len(files), passphrase, files)
		}
	}
}
