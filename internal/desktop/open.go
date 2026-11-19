// Package desktop hands things to the user's desktop: a URL to the default
// browser, a folder to the default file manager.
package desktop

import (
	"fmt"
	"os/exec"
	"runtime"
)

// Command is the platform's opener for target, which may be a URL or a path:
// open on macOS, which shows a folder in Finder; xdg-open elsewhere, which
// shows it in whatever file manager the desktop names as default. There is no
// portable way to do this, so it is one command per platform, and a headless
// box fails with its own words rather than hanging.
func Command(target string) *exec.Cmd {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", target)
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		return exec.Command("xdg-open", target)
	}
}

// Open starts the opener on target and does not wait for it.
func Open(target string) error {
	cmd := Command(target)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("%s: %w", cmd.Path, err)
	}
	// What it opens outlives us; reap the opener so it does not become a
	// zombie held by a program that is still running.
	go func() { _ = cmd.Wait() }()
	return nil
}
