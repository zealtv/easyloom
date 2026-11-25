// Package desktop hands things to the user's desktop: a path to the default
// opener, a folder to the default file manager.
package desktop

import (
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
