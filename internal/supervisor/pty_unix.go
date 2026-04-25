//go:build unix

package supervisor

import (
	"os"
	"os/exec"

	"github.com/creack/pty"
)

// spawnPTY starts cmd under a PTY. We do NOT set Setpgid here because
// pty.Start() sets Setsid=true internally, which already creates a new
// process group (setsid is a superset of setpgid). Setting both together
// is rejected by macOS Tahoe (26+). Since setsid makes pgid==pid,
// signalGroup(-pid, sig) works correctly for clean group kill.
func spawnPTY(cmd *exec.Cmd) (*os.File, error) {
	return pty.Start(cmd)
}
