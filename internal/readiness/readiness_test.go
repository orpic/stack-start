package readiness

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLogRegexCheck_Match(t *testing.T) {
	c, err := NewLogRegexCheck("listening on port \\d+")
	require.NoError(t, err)
	require.False(t, c.Satisfied())

	c.Evaluate([]byte("starting up..."))
	require.False(t, c.Satisfied())

	c.Evaluate([]byte("listening on port 4000"))
	require.True(t, c.Satisfied())
}

func TestLogRegexCheck_NoMatch(t *testing.T) {
	c, err := NewLogRegexCheck("READY")
	require.NoError(t, err)

	c.Evaluate([]byte("not ready yet"))
	c.Evaluate([]byte("still not"))
	require.False(t, c.Satisfied())
}

func TestLogRegexCheck_AlreadyMatched(t *testing.T) {
	c, err := NewLogRegexCheck("READY")
	require.NoError(t, err)

	c.Evaluate([]byte("READY"))
	require.True(t, c.Satisfied())

	c.Evaluate([]byte("something else"))
	require.True(t, c.Satisfied())
}

func TestLogRegexCheck_InvalidRegex(t *testing.T) {
	_, err := NewLogRegexCheck("[invalid")
	require.Error(t, err)
}

func TestTCPCheck_PortOpen(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()

	c := NewTCPCheck(ln.Addr().String())
	require.False(t, c.Satisfied())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go c.Run(ctx)

	require.Eventually(t, func() bool { return c.Satisfied() }, 2*time.Second, 50*time.Millisecond)
}

func TestTCPCheck_PortClosed(t *testing.T) {
	c := NewTCPCheck("127.0.0.1:19999")

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	go c.Run(ctx)

	time.Sleep(600 * time.Millisecond)
	require.False(t, c.Satisfied())
}

func TestEvaluator_AllMode(t *testing.T) {
	c1, _ := NewLogRegexCheck("A")
	c2, _ := NewLogRegexCheck("B")
	eval := NewEvaluator("all", []Check{c1, c2})

	require.False(t, eval.Satisfied())

	c1.Evaluate([]byte("A"))
	require.False(t, eval.Satisfied())

	c2.Evaluate([]byte("B"))
	require.True(t, eval.Satisfied())
}

func TestEvaluator_AnyMode(t *testing.T) {
	c1, _ := NewLogRegexCheck("A")
	c2, _ := NewLogRegexCheck("B")
	eval := NewEvaluator("any", []Check{c1, c2})

	require.False(t, eval.Satisfied())

	c1.Evaluate([]byte("A"))
	require.True(t, eval.Satisfied())
}

func TestEvaluator_EmptyChecks(t *testing.T) {
	eval := NewEvaluator("all", []Check{})
	require.True(t, eval.Satisfied())
}

func TestEvaluator_DefaultModeIsAll(t *testing.T) {
	eval := NewEvaluator("", []Check{})
	require.Equal(t, "all", eval.Mode)
}
