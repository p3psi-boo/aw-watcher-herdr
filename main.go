package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

type Config struct {
	Socket, Herdr, AW, Host                                                 string
	SelectionInterval, HeartbeatInterval, PulseTime, RetryInterval, Timeout time.Duration
	DryRun                                                                  bool
}

func parseConfig(args []string) (Config, error) {
	c := Config{}
	fs := flag.NewFlagSet("aw-watcher-herdr", flag.ContinueOnError)
	home, err := os.UserHomeDir()
	if err != nil {
		return c, err
	}
	root := os.Getenv("XDG_CONFIG_HOME")
	if root == "" {
		root = filepath.Join(home, ".config")
	}
	socket := os.Getenv("HERDR_SOCKET_PATH")
	if socket == "" {
		socket = filepath.Join(root, "herdr", "herdr.sock")
	}
	fs.StringVar(&c.Socket, "socket", socket, "local Herdr Unix socket (explicit path for a named session)")
	fs.StringVar(&c.Herdr, "herdr", "herdr", "Herdr CLI executable for machine discovery")
	fs.StringVar(&c.AW, "aw-url", "http://127.0.0.1:5600", "ActivityWatch base URL")
	hostname, err := os.Hostname()
	if err != nil {
		return c, err
	}
	fs.StringVar(&c.Host, "hostname", hostname, "collector identity in ActivityWatch buckets")
	fs.DurationVar(&c.SelectionInterval, "selection-interval", 0, "required: machine catalog polling interval")
	fs.DurationVar(&c.HeartbeatInterval, "heartbeat-interval", 0, "required: health check and ActivityWatch heartbeat interval")
	fs.DurationVar(&c.PulseTime, "pulsetime", 0, "required: ActivityWatch merge window, at least heartbeat-interval")
	fs.DurationVar(&c.RetryInterval, "retry-interval", 0, "required: delay before reconnecting")
	fs.DurationVar(&c.Timeout, "timeout", 0, "required: deadline for requests and local machine selection queries")
	fs.BoolVar(&c.DryRun, "dry-run", false, "print records as JSON to stdout instead of writing ActivityWatch")
	if err := fs.Parse(args); err != nil {
		return c, err
	}
	if fs.NArg() != 0 {
		return c, errors.New("unexpected positional arguments")
	}
	for name, d := range map[string]time.Duration{"selection-interval": c.SelectionInterval, "heartbeat-interval": c.HeartbeatInterval, "pulsetime": c.PulseTime, "retry-interval": c.RetryInterval, "timeout": c.Timeout} {
		if d <= 0 {
			return c, fmt.Errorf("--%s must be explicitly set to a positive duration", name)
		}
	}
	if c.PulseTime < c.HeartbeatInterval {
		return c, errors.New("--pulsetime must be at least --heartbeat-interval")
	}
	u, err := url.Parse(c.AW)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return c, errors.New("--aw-url must be an absolute HTTP(S) URL")
	}
	if c.Host == "" || c.Socket == "" {
		return c, errors.New("hostname and socket must not be empty")
	}
	return c, nil
}
func main() {
	c, err := parseConfig(os.Args[1:])
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, c); err != nil {
		slog.Error("watcher stopped", "error", err)
		os.Exit(1)
	}
}
