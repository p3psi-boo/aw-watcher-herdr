package main

import (
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConnectionDefaultsAndSocketPrecedence(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HERDR_SOCKET_PATH", "")
	c, err := parseConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	hostname, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	if c.Socket != filepath.Join(home, ".config", "herdr", "herdr.sock") || c.Herdr != "herdr" || c.AW != "http://127.0.0.1:5600" || c.Host != hostname || c.DryRun {
		t.Fatalf("unexpected defaults: %+v", c)
	}
	root := filepath.Join(home, "config")
	t.Setenv("XDG_CONFIG_HOME", root)
	c, err = parseConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Socket != filepath.Join(root, "herdr", "herdr.sock") {
		t.Fatal("XDG_CONFIG_HOME ignored")
	}
	socket := filepath.Join(home, "session.sock")
	t.Setenv("HERDR_SOCKET_PATH", socket)
	c, err = parseConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Socket != socket {
		t.Fatal("HERDR_SOCKET_PATH did not take precedence")
	}
	explicit := filepath.Join(home, "explicit.sock")
	c, err = parseConfig([]string{"--socket=" + explicit, "--aw-url=http://localhost:5600", "--herdr=/custom/herdr", "--hostname=custom", "--dry-run"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Socket != explicit || c.AW != "http://localhost:5600" || c.Herdr != "/custom/herdr" || c.Host != "custom" || !c.DryRun {
		t.Fatalf("overrides ignored: %+v", c)
	}
}

func TestInvalidConnectionOptions(t *testing.T) {
	for _, arg := range []string{"--herdr=", "--socket=", "--hostname=", "--aw-url=", "--aw-url=localhost:5600", "--aw-url=ftp://localhost", "unexpected"} {
		t.Run(arg, func(t *testing.T) {
			if _, err := parseConfig([]string{arg}); err == nil {
				t.Fatal("invalid option accepted")
			}
		})
	}
}

func TestHelpDoesNotRequireInstalledHerdr(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, err := parseConfig([]string{"--help"}); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("help returned %v", err)
	}
}

func TestVersionFlag(t *testing.T) {
	for _, f := range []string{"--version", "-version", "-v"} {
		c, err := parseConfig([]string{f})
		if err != nil {
			t.Fatalf("version flag %s failed: %v", f, err)
		}
		if !c.Version {
			t.Fatalf("expected Version=true for flag %s", f)
		}
	}
}

func TestMissingHerdrReportsRemedy(t *testing.T) {
	c := testConfig()
	c.Herdr = filepath.Join(t.TempDir(), "missing-herdr")
	err := run(context.Background(), c)
	if err == nil || !strings.Contains(err.Error(), "--herdr") || !strings.Contains(err.Error(), c.Herdr) {
		t.Fatalf("missing executable error lacks remedy: %v", err)
	}
}

func TestUnixSocketHomePaths(t *testing.T) {
	// These are path-resolution cases, not execution on both operating systems.
	for _, home := range []string{"/Users/example", "/home/example"} {
		t.Run(home, func(t *testing.T) {
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", "")
			t.Setenv("HERDR_SOCKET_PATH", "")
			c, err := parseConfig(nil)
			if err != nil {
				t.Fatal(err)
			}
			want := filepath.Join(home, ".config", "herdr", "herdr.sock")
			if c.Socket != want {
				t.Fatalf("socket = %q, want %q", c.Socket, want)
			}
		})
	}
}
