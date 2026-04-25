package readiness

import (
	"context"
	"net"
	"sync/atomic"
	"time"
)

// TCPCheck periodically attempts a TCP connection to host:port.
type TCPCheck struct {
	Address string
	matched atomic.Bool
}

func NewTCPCheck(address string) *TCPCheck {
	return &TCPCheck{Address: address}
}

func (c *TCPCheck) Run(ctx context.Context) {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			conn, err := net.DialTimeout("tcp", c.Address, time.Second)
			if err == nil {
				conn.Close()
				c.matched.Store(true)
				return
			}
		}
	}
}

func (c *TCPCheck) Satisfied() bool {
	return c.matched.Load()
}
