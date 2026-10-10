// Command fakeproc reproduces, deterministically, the ways real dev processes
// behave on a terminal, so stackstart can be tested against every ecosystem
// without installing them. Each mode names who really does it.
//
// Usage: fakeproc <mode> [arg]
package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		fail("usage: fakeproc <mode> [arg]")
	}
	mode, arg := os.Args[1], ""
	if len(os.Args) > 2 {
		arg = os.Args[2]
	}

	switch mode {
	case "prompt":
		// IEx, rails console, psql, node/python REPLs, "Ok to proceed? (y)".
		fmt.Print("Erlang/OTP 26 [erts-14.2] [64-bit]\r\n\r\n")
		fmt.Print("Interactive Elixir (1.17.3) - press Ctrl+C to exit\r\n")
		fmt.Print("iex(1)> ")
		block()

	case "clear":
		// tsc --watch, vite restart, nodemon, next dev, cargo watch -c, air.
		fmt.Println("build 1 ok")
		fmt.Println("error from build 1")
		time.Sleep(300 * time.Millisecond)
		fmt.Print("\x1b[2J\x1b[3J\x1b[H")
		fmt.Println("build 2 ok")
		block()

	case "selfrestart":
		// nodemon / node --watch / Phoenix code reloader restarting in place.
		fmt.Println("[nodemon] starting `node server.js`")
		fmt.Println("listening on 4000")
		for i := 1; i <= 2; i++ {
			time.Sleep(200 * time.Millisecond)
			fmt.Printf("\x1bc[nodemon] restarting due to changes... (%d)\n", i)
			fmt.Println("listening on 4000")
		}
		block()

	case "spinner":
		// npm install, docker pull, esbuild, webpack, cargo, mix deps.get.
		fmt.Print("| 10%\r/ 50%\r- 100% done\n")
		fmt.Print("\x1b[32mdownloading\x1b[0m 30%\r\x1b[Kdownloaded\n")
		fmt.Println("installed")
		block()

	case "redraw":
		// docker compose, Next.js, Turborepo, ink-based CLIs.
		fmt.Println("[+] Running 0/3")
		fmt.Print("\x1b[1A\x1b[0G\x1b[2K[+] Running 3/3\n")
		fmt.Print("\x1b]0;compose: up\x07 \x1b[?25l✔ Container db  Started\x1b[?25h\n")
		fmt.Println("compose ready")
		block()

	case "longline":
		// Minified stack traces, JSON loggers, webpack stats, Elixir inspect.
		n := atoi(arg, 100_000)
		fmt.Println("started")
		fmt.Println(strings.Repeat("x", n))
		for i := 1; i <= 3000; i++ {
			fmt.Printf("normal line %d\n", i)
		}
		fmt.Println("DONE_WRITING")
		block()

	case "silentport":
		// Postgres, Redis, many Go/Rust/Elixir servers: open a port, say nothing.
		fmt.Println("booting")
		time.Sleep(500 * time.Millisecond)
		ln, err := net.Listen("tcp", "127.0.0.1:"+arg)
		if err != nil {
			fail(err.Error())
		}
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				_ = c.Close()
			}
		}()
		block()

	case "chatty":
		// Keeps logging but never prints its ready line.
		for i := 0; ; i++ {
			fmt.Printf("still booting %d\n", i)
			time.Sleep(100 * time.Millisecond)
		}

	case "quiet":
		// Prints one line, then nothing, and never becomes ready.
		fmt.Println("waiting for something")
		block()

	case "tree":
		// npm run dev -> node -> esbuild; mix phx.server -> esbuild/tailwind.
		child := exec.Command(os.Args[0], "sleepchild")
		if err := child.Start(); err != nil {
			fail(err.Error())
		}
		if err := os.WriteFile(arg, []byte(strconv.Itoa(child.Process.Pid)), 0644); err != nil {
			fail(err.Error())
		}
		fmt.Println("parent ready")
		block()

	case "escape":
		// BEAM port programs (Phoenix esbuild/tailwind watchers) and anything
		// that calls setsid: the child leaves the parent's process group.
		child := exec.Command(os.Args[0], "sleepchild")
		child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := child.Start(); err != nil {
			fail(err.Error())
		}
		if err := os.WriteFile(arg, []byte(strconv.Itoa(child.Process.Pid)), 0644); err != nil {
			fail(err.Error())
		}
		fmt.Println("parent ready")
		block()

	case "sleepchild":
		block()

	case "stubborn":
		// BEAM/Puma/JVM apps with slow or broken shutdown: ignores SIGTERM.
		sig := make(chan os.Signal, 4)
		signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
		fmt.Println("stubborn ready")
		for s := range sig {
			fmt.Printf("ignoring %s\n", s)
		}

	case "readythencrash":
		// Bad migration, port taken after boot, tunnel dropping.
		fmt.Println("ready")
		time.Sleep(300 * time.Millisecond)
		fmt.Println("** (Postgrex.Error) column \"organization_id\" contains null values")
		os.Exit(1)

	case "tunnelfail":
		// cloudflared failing: the error line contains a trycloudflare.com URL.
		fmt.Println("INF Requesting new quick Tunnel on trycloudflare.com...")
		time.Sleep(200 * time.Millisecond)
		fmt.Println(`ERR failed to request quick Tunnel: Post "https://api.trycloudflare.com/tunnel": context deadline exceeded`)
		block()

	case "tunnelok":
		// cloudflared succeeding, with its box-drawing banner.
		fmt.Println("INF Requesting new quick Tunnel on trycloudflare.com...")
		fmt.Println("INF +--------------------------------------------------------------------------------------------+")
		fmt.Println("INF |  Your quick Tunnel has been created! Visit it at (it may take some time to be reachable):  |")
		fmt.Println("INF |  https://quiet-river-blue-moon.trycloudflare.com                                           |")
		fmt.Println("INF +--------------------------------------------------------------------------------------------+")
		block()

	case "unicode":
		// Vite/Next banners, cloudflared boxes, emoji.
		fmt.Println("  VITE v5.0.0  ready in 423 ms")
		fmt.Println("  ➜  Local:   http://localhost:5173/")
		fmt.Println("  ✓ Compiled │ 🚀 done")
		block()

	case "binary":
		// Crashed native code, cat of a binary file.
		_, _ = os.Stdout.Write([]byte("garbage \xff\xfe\x00\x01 bytes\n"))
		fmt.Println("after binary")
		block()

	case "burst":
		// webpack --verbose, test runners, SQL debug logging.
		n := atoi(arg, 50_000)
		for i := 1; i <= n; i++ {
			fmt.Printf("burst line %d\n", i)
		}
		fmt.Println("BURST_DONE")
		block()

	case "stderr":
		// Go log, Python logging, Elixir Logger: stderr only.
		fmt.Fprintln(os.Stderr, "stderr ready")
		block()

	case "pager":
		// psql, git log, mysql, man: on a terminal they pipe output into
		// $PAGER (less) and wait for a keypress nobody will make.
		if p := os.Getenv("PAGER"); p != "cat" {
			fmt.Printf("result row 1\n(END)")
			block()
		}
		fmt.Println("result row 1")

	case "oneshot":
		// mix ecto.migrate, npm run build, rails db:migrate, prisma generate.
		fmt.Println("migrated")

	default:
		fail("unknown mode " + mode)
	}
}

// block waits until killed. (A bare `select {}` would trip Go's deadlock
// detector when no other goroutine is running.)
func block() {
	for {
		time.Sleep(time.Hour)
	}
}

func atoi(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, "fakeproc: "+msg)
	os.Exit(2)
}
