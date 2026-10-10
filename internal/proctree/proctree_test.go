package proctree

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParseProcStat(t *testing.T) {
	pid, ppid, ok := parseProcStat([]byte("1234 (weird (name) x) S 99 1234 1234 0 -1"))
	require.True(t, ok)
	require.Equal(t, 1234, pid)
	require.Equal(t, 99, ppid)

	_, _, ok = parseProcStat([]byte("garbage"))
	require.False(t, ok)
}

func TestParsePS(t *testing.T) {
	m := parsePS([]byte("    1     0\n  100     1\n  101   100\n\nbad line here\n"))
	require.Equal(t, map[int]int{1: 0, 100: 1, 101: 100}, m)
}

func TestDescendantsFindsChildrenInOtherSessions(t *testing.T) {
	// A shell that starts one ordinary child and one in its own session
	// (like a BEAM port program), then waits.
	cmd := exec.Command("/bin/sh", "-c", `sleep 300 & `+os.Args[0]+` -test.run=TestHelperSetsidChild & wait`)
	cmd.Env = append(os.Environ(), "PROCTREE_HELPER=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	require.NoError(t, cmd.Start())
	defer func() {
		desc, _ := Descendants(cmd.Process.Pid)
		for _, p := range desc {
			_ = syscall.Kill(p, syscall.SIGKILL)
		}
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	var desc []int
	require.Eventually(t, func() bool {
		var err error
		if desc, err = Descendants(cmd.Process.Pid); err != nil {
			return false
		}
		for _, p := range desc {
			if pgid, err := syscall.Getpgid(p); err == nil && pgid != cmd.Process.Pid {
				return true
			}
		}
		return false
	}, 10*time.Second, 50*time.Millisecond,
		"the setsid child must be found as a descendant outside the process group")
}

// TestHelperSetsidChild is run as a subprocess by the test above.
func TestHelperSetsidChild(t *testing.T) {
	if os.Getenv("PROCTREE_HELPER") != "1" {
		t.Skip("helper process")
	}
	child := exec.Command("sleep", "300")
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := child.Start(); err != nil {
		os.Exit(1)
	}
	_ = child.Wait()
}

func TestDescendantsOfLeafIsEmpty(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	require.NoError(t, cmd.Start())
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	desc, err := Descendants(cmd.Process.Pid)
	require.NoError(t, err)
	require.Empty(t, desc)
}
