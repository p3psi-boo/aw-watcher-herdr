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

const appVersion = "0.2.0"

type Config struct {
	Socket, Herdr, AW, Host                                                 string
	SelectionInterval, HeartbeatInterval, PulseTime, RetryInterval, Timeout time.Duration
	DryRun, Version                                                         bool
}

func parseConfig(args []string) (Config, error) {
	c := Config{}
	fs := flag.NewFlagSet("aw-watcher-herdr", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: aw-watcher-herdr [options]\n       aw-watcher-herdr doctor [options]\n       aw-watcher-herdr service <command> [options]\n\nRun without options to record local Herdr activity.\nUse 'doctor' to diagnose environment, sockets, and service connectivity.\nUse 'service --help' for background installation and management.\n\nOptions:")
		fs.PrintDefaults()
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return c, err
	}
	root := os.Getenv("XDG_CONFIG_HOME")
	if root == "" {
		// Herdr uses ~/.config on both macOS and Linux, not os.UserConfigDir().
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
	fs.DurationVar(&c.SelectionInterval, "selection-interval", time.Second, "machine catalog polling interval")
	fs.DurationVar(&c.HeartbeatInterval, "heartbeat-interval", time.Second, "health check and ActivityWatch heartbeat interval")
	fs.DurationVar(&c.PulseTime, "pulsetime", 2*time.Second, "ActivityWatch merge window; when omitted, max(heartbeat-interval*1.5, heartbeat-interval+1s)")
	fs.DurationVar(&c.RetryInterval, "retry-interval", 10*time.Second, "delay before reconnecting")
	fs.DurationVar(&c.Timeout, "timeout", 5*time.Second, "deadline for requests and local machine selection queries")
	fs.BoolVar(&c.DryRun, "dry-run", false, "print records as JSON to stdout instead of writing ActivityWatch")
	fs.BoolVar(&c.Version, "version", false, "print version and exit")
	fs.BoolVar(&c.Version, "v", false, "print version and exit")
	if err := fs.Parse(args); err != nil {
		return c, err
	}
	if c.Version {
		return c, nil
	}
	if fs.NArg() != 0 {
		return c, errors.New("unexpected positional arguments")
	}
	pulseExplicit := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "pulsetime" {
			pulseExplicit = true
		}
	})
	if !pulseExplicit {
		c.PulseTime = c.HeartbeatInterval + max(c.HeartbeatInterval/2, time.Second)
	}
	for name, d := range map[string]time.Duration{"selection-interval": c.SelectionInterval, "heartbeat-interval": c.HeartbeatInterval, "pulsetime": c.PulseTime, "retry-interval": c.RetryInterval, "timeout": c.Timeout} {
		if d <= 0 {
			return c, fmt.Errorf("--%s must be a positive duration", name)
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
	if c.Herdr == "" {
		return c, errors.New("--herdr must not be empty; omit it to find herdr in PATH")
	}
	return c, nil
}
func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "service":
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			if err := serviceCLI(ctx, os.Args[2:], os.Stdout); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			return
		case "doctor":
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			c, err := parseConfig(os.Args[2:])
			if errors.Is(err, flag.ErrHelp) {
				return
			}
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(2)
			}
			if err := runDoctor(ctx, c, os.Stdout); err != nil {
				os.Exit(1)
			}
			return
		}
	}
	c, err := parseConfig(os.Args[1:])
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if c.Version {
		fmt.Println("aw-watcher-herdr v" + appVersion)
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if !c.DryRun {
		slog.Info("aw-watcher-herdr starting", "version", appVersion, "socket", c.Socket, "aw", c.AW, "host", c.Host)
	}
	if err := run(ctx, c); err != nil {
		slog.Error("watcher stopped", "error", err)
		os.Exit(1)
	}
}
