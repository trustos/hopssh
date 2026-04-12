//go:build windows

package main

import (
	"embed"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
)

//go:embed wintun/amd64/wintun.dll wintun/arm64/wintun.dll
var wintunFS embed.FS

// ensureWinTun extracts the embedded wintun.dll to the path Nebula expects:
// <exe_dir>/dist/windows/wintun/bin/<arch>/wintun.dll
// This is checked by checkWinTunExists() in nebula/overlay/tun_windows.go.
func ensureWinTun() error {
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("get executable path: %w", err)
	}
	exeDir := filepath.Dir(exePath)

	arch := runtime.GOARCH
	if arch != "amd64" && arch != "arm64" {
		return fmt.Errorf("unsupported architecture for wintun: %s", arch)
	}

	// Path Nebula's checkWinTunExists() expects.
	nebulaArch := arch
	destDir := filepath.Join(exeDir, "dist", "windows", "wintun", "bin", nebulaArch)
	destPath := filepath.Join(destDir, "wintun.dll")

	// Skip if already extracted.
	if info, err := os.Stat(destPath); err == nil && info.Size() > 0 {
		return nil
	}

	dllData, err := wintunFS.ReadFile(fmt.Sprintf("wintun/%s/wintun.dll", arch))
	if err != nil {
		return fmt.Errorf("read embedded wintun.dll: %w", err)
	}

	if err := os.MkdirAll(destDir, 0755); err != nil {
		return fmt.Errorf("create wintun dir %s: %w", destDir, err)
	}

	if err := os.WriteFile(destPath, dllData, 0644); err != nil {
		return fmt.Errorf("write wintun.dll to %s: %w", destPath, err)
	}

	log.Printf("[agent] extracted wintun.dll to %s", destPath)
	return nil
}
