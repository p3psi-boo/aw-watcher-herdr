package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

func runDoctor(ctx context.Context, c Config, out io.Writer) error {
	hasError := false

	fmt.Fprintln(out, "aw-watcher-herdr doctor")
	fmt.Fprintln(out, "=======================")

	// 1. Herdr CLI executable check
	herdrPath, err := exec.LookPath(c.Herdr)
	if err != nil {
		fmt.Fprintf(out, "[✗] Herdr CLI: %q not found in PATH\n    Hint: install Herdr or pass --herdr to its executable path\n", c.Herdr)
		hasError = true
	} else {
		cmdCtx, cancel := context.WithTimeout(ctx, c.Timeout)
		verOut, verErr := exec.CommandContext(cmdCtx, herdrPath, "--version").Output()
		cancel()
		verStr := strings.TrimSpace(string(verOut))
		if verErr != nil || verStr == "" {
			verStr = "unknown version"
		}
		fmt.Fprintf(out, "[✓] Herdr CLI: %s (%s)\n", herdrPath, verStr)
	}

	// 2. Herdr Unix socket connectivity & snapshot check
	s, err := snapshot(ctx, c.Socket, c.Timeout)
	if err != nil {
		fmt.Fprintf(out, "[✗] Herdr socket: %s (unreachable: %v)\n    Hint: ensure Herdr server is running or check --socket\n", c.Socket, err)
		hasError = true
	} else {
		fmt.Fprintf(out, "[✓] Herdr socket: %s (connected: %d workspace(s), %d pane(s))\n", c.Socket, len(s.Workspaces), len(s.Panes))
	}

	// 3. ActivityWatch HTTP API check
	awURL := strings.TrimRight(c.AW, "/") + "/api/0/info"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, awURL, nil)
	if err != nil {
		fmt.Fprintf(out, "[✗] ActivityWatch API: %s (invalid URL: %v)\n", c.AW, err)
		hasError = true
	} else {
		client := &http.Client{Timeout: c.Timeout}
		resp, err := client.Do(req)
		if err != nil {
			fmt.Fprintf(out, "[✗] ActivityWatch API: %s (unreachable: %v)\n    Hint: ensure ActivityWatch is running on %s\n", c.AW, err, c.AW)
			hasError = true
		} else {
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				fmt.Fprintf(out, "[✗] ActivityWatch API: %s (HTTP %d)\n", c.AW, resp.StatusCode)
				hasError = true
			} else {
				var info struct {
					Version  string `json:"version"`
					Hostname string `json:"hostname"`
				}
				_ = json.NewDecoder(resp.Body).Decode(&info)
				ver := info.Version
				if ver == "" {
					ver = "responsive"
				} else {
					ver = "v" + strings.TrimPrefix(ver, "v")
				}
				fmt.Fprintf(out, "[✓] ActivityWatch API: %s (%s)\n", c.AW, ver)
			}
		}
	}

	// 4. Background service status
	home, homeErr := os.UserHomeDir()
	if homeErr == nil {
		layout, sErr := servicePaths(runtime.GOOS, home, os.Getenv("XDG_CONFIG_HOME"), os.Getuid())
		if sErr == nil {
			if info, err := os.Lstat(layout.config); err == nil && info.Mode().IsRegular() {
				fmt.Fprintf(out, "[✓] Background service: installed (%s)\n", layout.config)
			} else {
				fmt.Fprintf(out, "[-] Background service: not installed\n    Hint: run 'aw-watcher-herdr service install' to enable automatic startup\n")
			}
		}
	}

	fmt.Fprintln(out, "-----------------------")
	if hasError {
		fmt.Fprintln(out, "Result: Issues detected. Please resolve the errors above.")
		return fmt.Errorf("doctor check failed")
	}
	fmt.Fprintln(out, "Result: All checks passed. Ready to record activity.")
	return nil
}
