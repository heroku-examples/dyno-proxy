package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// runStartupHook executes an optional `dyno-proxy-start-hook` executable located in
// the same directory as the running dyno-proxy binary. It is run directly (so it
// may be a script with a shebang or a compiled binary) and must have its
// executable bit set. It runs synchronously; startup blocks until it completes.
// When no such file exists this is a no-op. The hook's stdout/stderr are inherited so its output is visible.
func runStartupHook() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("run: locating dyno-proxy executable for startup hook: %w", err)
	}

	hook := filepath.Join(filepath.Dir(exe), "dyno-proxy-start-hook")
	info, err := os.Stat(hook)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("run: checking for startup hook %q: %w", hook, err)
	}
	if info.IsDir() {
		return nil
	}

	cmd := exec.Command(hook)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("run: startup hook %q failed: %w", hook, err)
	}
	return nil
}
