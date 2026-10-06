// Package open shows a file in the operating system's default app.
package open

import (
	"os"
	"os/exec"
	"runtime"
)

// File opens path with the default app for its type.
func File(path string) error {
	if _, err := os.Stat(path); err != nil {
		return err
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", path)
	case "darwin":
		cmd = exec.Command("open", path)
	default:
		cmd = exec.Command("xdg-open", path)
	}
	return cmd.Start()
}
