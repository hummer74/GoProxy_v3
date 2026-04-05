package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	windows "golang.org/x/sys/windows"
)

// fixKeyPermissions attempts to fix SSH key file permissions using icacls
func fixKeyPermissions(keyPath string) bool {
	if keyPath == "" {
		return true
	}

	// Normalize path separators
	keyPath = filepath.FromSlash(keyPath)

	// Check if file exists
	if _, err := os.Stat(keyPath); os.IsNotExist(err) {
		return false
	}

	// First try a simple approach - just set basic permissions
	simpleCommands := [][]string{
		{"icacls", keyPath, "/reset"},
		{"icacls", keyPath, "/inheritance:r"},
		{"icacls", keyPath, "/grant:r", fmt.Sprintf("%s:F", os.Getenv("USERNAME"))},
	}

	for _, args := range simpleCommands {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.SysProcAttr = &windows.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
		_ = cmd.Run()
	}

	// Try alternative method using PowerShell with explicit error handling
	psScript := fmt.Sprintf(`
try {
    $keyPath = "%s"
    $acl = Get-Acl -Path $keyPath
    $permissions = $acl.Access | Where-Object { $_.IdentityReference -notlike "*%s*" }
    foreach ($perm in $permissions) {
        $acl.RemoveAccessRule($perm) | Out-Null
    }
    $rule = New-Object System.Security.AccessControl.FileSystemAccessRule("%s","FullControl","Allow")
    $acl.SetAccessRule($rule)
    Set-Acl -Path $keyPath -AclObject $acl
    Write-Output "Permissions fixed successfully"
    exit 0
} catch {
    Write-Error $_.Exception.Message
    exit 1
}`, strings.ReplaceAll(keyPath, `\`, `\\`), os.Getenv("USERNAME"), os.Getenv("USERNAME"))

	psCmd := exec.Command("powershell", "-ExecutionPolicy", "Bypass", "-Command", psScript)
	psCmd.SysProcAttr = &windows.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
	_ = psCmd.Run()

	// Final check - can we read the file?
	if _, err := os.ReadFile(keyPath); err != nil {
		// Last resort: try to copy the file to a temp location with proper permissions
		tempKeyPath := filepath.Join(os.TempDir(), fmt.Sprintf("ssh_key_%d.tmp", os.Getpid()))
		if err := copyFileToTemp(keyPath, tempKeyPath); err == nil {
			// Update the keyPath to use the temp file
			keyPath = tempKeyPath
			return true
		}
		return false
	}

	return true
}

// copyFileToTemp copies a file to temp location and sets proper permissions
func copyFileToTemp(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}

	if err := os.WriteFile(dst, data, 0600); err != nil {
		return err
	}

	// Set proper permissions on the copied file
	cmd := exec.Command("icacls", dst, "/inheritance:r", "/grant:r", fmt.Sprintf("%s:F", os.Getenv("USERNAME")))
	cmd.SysProcAttr = &windows.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
	_, _ = cmd.CombinedOutput()

	return nil
}
