package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/macula-io/macula-go-sdk/connection"
	"github.com/macula-io/macula-go-sdk/transport"

	"github.com/macula-io/macula-cli/internal/daemon"
	"github.com/macula-io/macula-cli/internal/report"
)

func runDaemon(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: macula-cli daemon start|status|stop [flags]")
		return 2
	}
	switch args[0] {
	case "start":
		return runDaemonStart(args[1:])
	case "status":
		return runDaemonStatus(args[1:])
	case "stop":
		return runDaemonStop(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "macula-cli daemon: unknown subcommand %q (want start, status, or stop)\n", args[0])
		return 2
	}
}

// resolveSocketPath is shared by all three daemon subcommands so
// -socket and -socket-name always mean the same thing everywhere.
func resolveSocketPath(explicit, name string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	return daemon.SocketPath(name)
}

func runDaemonStart(args []string) int {
	fs := flag.NewFlagSet("daemon start", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "emit a JSON result envelope instead of human-readable text")
	identityPath := fs.String("identity", "", "path to a persisted identity seed (default: config dir)")
	socketName := fs.String("socket-name", daemon.DefaultName, "name this daemon instance under (lets more than one run side by side)")
	socketPath := fs.String("socket", "", "control socket path (default: derived from -socket-name)")
	connectTimeout := fs.Duration("connect-timeout", 15*time.Second, "connect timeout")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "Usage: macula-cli daemon start [flags] <host[:port]>\n\n"+
			"Connects once and holds the Session open, serving mesh CALLs against\n"+
			"whatever \"serve --daemon\" registers, until stopped (\"daemon stop\",\n"+
			"Ctrl-C, or SIGTERM). Other macula-cli invocations control this daemon\n"+
			"over a local control socket instead of dialing the mesh themselves.\n"+
			"Runs in the foreground -- pair with a process supervisor (systemd,\n"+
			"a Windows Service wrapper) for unattended use.\n\nFlags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return 2
	}

	host, port, err := parseHostPort(fs.Arg(0))
	if err != nil {
		return report.Fail(*jsonOut, err, nil)
	}
	sockPath, err := resolveSocketPath(*socketPath, *socketName)
	if err != nil {
		return report.Fail(*jsonOut, err, nil)
	}

	id, generated, err := loadIdentity(*identityPath)
	if err != nil {
		return report.Fail(*jsonOut, err, nil)
	}
	if generated && !*jsonOut {
		fmt.Println("(generated a new identity — puzzle grinding took a moment)")
	}

	connectCtx, connectCancel := context.WithTimeout(context.Background(), *connectTimeout)
	defer connectCancel()
	session, err := connection.Connect(connectCtx, host, port, transport.WebPKI{}, id)
	if err != nil {
		return report.Fail(*jsonOut, err, nil)
	}
	defer session.Close("normal", nil, id)

	connectedTo := fmt.Sprintf("%s:%d", host, port)
	srv := daemon.NewServer(session, id, connectedTo)

	if !*jsonOut {
		fmt.Printf("daemon started: identity=%s connected_to=%s socket=%s\n", hexNodeID(id), connectedTo, sockPath)
		fmt.Println("(Ctrl-C, SIGTERM, or \"macula-cli daemon stop\" to stop)")
	} else {
		report.Ok(true, map[string]any{"identity": hexNodeID(id), "connected_to": connectedTo, "socket": sockPath}, nil)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	go func() {
		<-sigCh
		cancel()
	}()

	if err := srv.Run(ctx, sockPath); err != nil && !errors.Is(err, context.Canceled) {
		// Run returns exactly context.Canceled (its own internal
		// context's Err()) for BOTH a signal cancelling the ctx passed
		// in here AND a "daemon stop" RPC calling Shutdown() directly
		// on Run's own child context -- either way that's a clean,
		// requested stop, not a failure worth reporting as one. Any
		// other error is a genuine failure (mesh serve loop died,
		// control socket died).
		return report.Fail(*jsonOut, err, nil)
	}
	if !*jsonOut {
		fmt.Println("daemon stopped")
	}
	return 0
}

func runDaemonStatus(args []string) int {
	fs := flag.NewFlagSet("daemon status", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "emit a JSON result envelope instead of human-readable text")
	socketName := fs.String("socket-name", daemon.DefaultName, "the daemon instance's -socket-name")
	socketPath := fs.String("socket", "", "control socket path (default: derived from -socket-name)")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "Usage: macula-cli daemon status [flags]\n\nFlags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	sockPath, err := resolveSocketPath(*socketPath, *socketName)
	if err != nil {
		return report.Fail(*jsonOut, err, nil)
	}

	var result daemon.StatusResult
	if err := daemon.Do(sockPath, daemon.MethodStatus, nil, &result); err != nil {
		return report.Fail(*jsonOut, err, nil)
	}
	report.Ok(*jsonOut, result, func() {
		fmt.Printf("identity:     %s\n", result.Identity)
		fmt.Printf("connected to: %s\n", result.ConnectedTo)
		fmt.Printf("uptime:       %ds\n", result.UptimeSeconds)
		if len(result.Serving) == 0 {
			fmt.Println("serving:      (nothing registered)")
		} else {
			fmt.Println("serving:")
			for _, p := range result.Serving {
				fmt.Printf("  - %s\n", p)
			}
		}
	})
	return 0
}

func runDaemonStop(args []string) int {
	fs := flag.NewFlagSet("daemon stop", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "emit a JSON result envelope instead of human-readable text")
	socketName := fs.String("socket-name", daemon.DefaultName, "the daemon instance's -socket-name")
	socketPath := fs.String("socket", "", "control socket path (default: derived from -socket-name)")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "Usage: macula-cli daemon stop [flags]\n\nFlags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	sockPath, err := resolveSocketPath(*socketPath, *socketName)
	if err != nil {
		return report.Fail(*jsonOut, err, nil)
	}

	var result daemon.ShutdownResult
	if err := daemon.Do(sockPath, daemon.MethodShutdown, nil, &result); err != nil {
		return report.Fail(*jsonOut, err, nil)
	}
	report.Ok(*jsonOut, result, func() {
		fmt.Println("daemon stopping")
	})
	return 0
}
