package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const serviceUsage = `Usage:
  aw-watcher-herdr service install [watcher options]
  aw-watcher-herdr service install --dry-run [watcher options]
  aw-watcher-herdr service start|status|stop|restart|uninstall

install creates a user service, enables login startup and starts recording.
install --dry-run only prints the generated service configuration.
start enables login startup and starts the installed service.
stop stops the service and disables login startup until start is called.
restart restarts the installed service.
uninstall stops the service and removes only its generated configuration.
Existing external configurations are never overwritten or removed.
macOS requires a GUI login; Linux requires a systemd user manager.
`

type serviceRunner func(context.Context, string, ...string) ([]byte, error)

func serviceExec(ctx context.Context, name string, args ...string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("%s %s: %w\n%s", name, strings.Join(args, " "), err, out)
	}
	return out, nil
}

type serviceManager struct {
	layout  serviceLayout
	command serviceRunner
	out     io.Writer
}

func serviceCLI(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprint(out, serviceUsage)
		return nil
	}
	action := args[0]
	switch action {
	case "install":
	case "start", "status", "stop", "restart", "uninstall":
		if len(args) != 1 {
			return fmt.Errorf("service %s takes no options; use service install to save watcher options", action)
		}
	default:
		return fmt.Errorf("unknown service command %q\n%s", action, serviceUsage)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	layout, err := servicePaths(runtime.GOOS, home, os.Getenv("XDG_CONFIG_HOME"), os.Getuid())
	if err != nil {
		return err
	}
	m := serviceManager{layout, serviceExec, out}
	if action != "install" {
		return m.manage(ctx, action)
	}
	c, err := parseConfig(args[1:])
	if errors.Is(err, flag.ErrHelp) {
		fmt.Fprint(out, serviceUsage)
		return nil
	}
	if err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	c.Herdr, err = exec.LookPath(c.Herdr)
	if err != nil {
		return fmt.Errorf("find Herdr: %w; install Herdr or pass --herdr", err)
	}
	c.Herdr, err = filepath.Abs(c.Herdr)
	if err != nil {
		return err
	}
	c.Socket, err = filepath.Abs(c.Socket)
	if err != nil {
		return err
	}
	// Save only environment needed by Herdr, rather than unrelated tokens/secrets.
	env := map[string]string{"HOME": home, "HERDR_SOCKET_PATH": c.Socket}
	for _, key := range []string{"XDG_CONFIG_HOME", "HERDR_CONFIG_PATH"} {
		if value := os.Getenv(key); value != "" {
			absolute, err := filepath.Abs(value)
			if err != nil {
				return err
			}
			env[key] = absolute
		}
	}
	data, err := renderService(layout, serviceArguments(executable, c), env, c.RetryInterval.Seconds())
	if err != nil {
		return err
	}
	if c.DryRun {
		_, err = out.Write(data)
		return err
	}
	return m.install(ctx, data)
}

// A symlink may belong to Nix/Home Manager; never follow it for management.
func (m serviceManager) ownedConfig() ([]byte, error) {
	info, err := os.Lstat(m.layout.config)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("service is not installed; run service install")
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("service configuration %s is externally managed; use its original service manager", m.layout.config)
	}
	data, err := os.ReadFile(m.layout.config)
	if err != nil {
		return nil, err
	}
	prefix := "# " + serviceMarker + "\n"
	if m.layout.platform == "darwin" {
		prefix = "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<!-- " + serviceMarker + " -->\n"
	}
	if !bytes.HasPrefix(data, []byte(prefix)) {
		return nil, fmt.Errorf("service configuration %s is externally managed; use its original service manager", m.layout.config)
	}
	return data, nil
}

func (m serviceManager) call(ctx context.Context, args ...string) error {
	name := "launchctl"
	if m.layout.platform == "linux" {
		name = "systemctl"
		args = append([]string{"--user"}, args...)
	}
	out, err := m.command(ctx, name, args...)
	if len(out) > 0 {
		fmt.Fprint(m.out, string(out))
	}
	return err
}

func (m serviceManager) install(ctx context.Context, data []byte) error {
	if _, err := os.Lstat(m.layout.config); err == nil {
		existing, err := m.ownedConfig()
		if err != nil {
			return err
		}
		if bytes.Equal(existing, data) {
			fmt.Fprintln(m.out, "Service is already installed with these options; use service start or service status.")
			return nil
		}
		return fmt.Errorf("service is already installed with different options; run service uninstall, then service install with the new options")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// Check the manager before creating files, including services loaded elsewhere.
	if m.layout.platform == "darwin" {
		if _, err := m.command(ctx, "launchctl", "print", m.layout.domain); err != nil {
			return fmt.Errorf("macOS GUI user domain is unavailable: %w", err)
		}
		if _, err := m.command(ctx, "launchctl", "print", m.layout.domain+"/"+serviceLabel); err == nil {
			return fmt.Errorf("a service with label %s is already loaded; use its original service manager", serviceLabel)
		}
	} else {
		out, err := m.command(ctx, "systemctl", "--user", "show", serviceUnit, "--property=LoadState", "--value")
		if err != nil {
			return fmt.Errorf("systemd user manager is unavailable: %w", err)
		}
		if strings.TrimSpace(string(out)) != "not-found" {
			return fmt.Errorf("a unit named %s already exists; use its original service manager", serviceUnit)
		}
	}
	if err := os.MkdirAll(filepath.Dir(m.layout.config), 0700); err != nil {
		return err
	}
	if m.layout.logDir != "" {
		if err := os.MkdirAll(m.layout.logDir, 0700); err != nil {
			return err
		}
	}
	// O_EXCL also protects files or symlinks created after the initial check.
	file, err := os.OpenFile(m.layout.config, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return errors.Join(err, os.Remove(m.layout.config))
	}
	fmt.Fprintln(m.out, "Created", m.layout.config)
	if err := m.start(ctx); err != nil {
		return fmt.Errorf("configuration saved but service startup failed; inspect service status and retry service start: %w", err)
	}
	fmt.Fprintln(m.out, "Service startup requested; use service status to inspect the process.")
	return nil
}

func (m serviceManager) start(ctx context.Context) error {
	if m.layout.platform == "linux" {
		if err := m.call(ctx, "daemon-reload"); err != nil {
			return err
		}
		return m.call(ctx, "enable", "--now", serviceUnit)
	}
	target := m.layout.domain + "/" + serviceLabel
	if err := m.call(ctx, "enable", target); err != nil {
		return err
	}
	if _, err := m.command(ctx, "launchctl", "print", target); err == nil {
		return m.call(ctx, "kickstart", target)
	}
	return m.call(ctx, "bootstrap", m.layout.domain, m.layout.config)
}

func (m serviceManager) stop(ctx context.Context) error {
	if m.layout.platform == "linux" {
		return m.call(ctx, "disable", "--now", serviceUnit)
	}
	target := m.layout.domain + "/" + serviceLabel
	if err := m.call(ctx, "disable", target); err != nil {
		return err
	}
	if _, err := m.command(ctx, "launchctl", "print", target); err != nil {
		return nil
	}
	return m.call(ctx, "bootout", target)
}

func (m serviceManager) restart(ctx context.Context) error {
	if m.layout.platform == "linux" {
		if err := m.call(ctx, "daemon-reload"); err != nil {
			return err
		}
		return m.call(ctx, "restart", serviceUnit)
	}
	target := m.layout.domain + "/" + serviceLabel
	if _, err := m.command(ctx, "launchctl", "print", target); err == nil {
		return m.call(ctx, "kickstart", "-k", target)
	}
	return m.start(ctx)
}

func (m serviceManager) manage(ctx context.Context, action string) error {
	if _, err := m.ownedConfig(); err != nil {
		return err
	}
	switch action {
	case "status":
		fmt.Fprintln(m.out, "Configuration:", m.layout.config)
		if m.layout.platform == "linux" {
			fmt.Fprintln(m.out, "Logs: journalctl --user -u", serviceUnit)
			return m.call(ctx, "status", "--no-pager", serviceUnit)
		}
		fmt.Fprintln(m.out, "Logs:", m.layout.logDir)
		return m.call(ctx, "print", m.layout.domain+"/"+serviceLabel)
	case "start":
		return m.start(ctx)
	case "stop":
		return m.stop(ctx)
	case "restart":
		return m.restart(ctx)
	case "uninstall":
		if err := m.stop(ctx); err != nil {
			return err
		}
		if err := os.Remove(m.layout.config); err != nil {
			return err
		}
		if m.layout.platform == "linux" {
			if err := m.call(ctx, "daemon-reload"); err != nil {
				return err
			}
		}
		fmt.Fprintln(m.out, "Removed service configuration; binaries, logs and ActivityWatch data were preserved.")
		return nil
	}
	return fmt.Errorf("unknown service command %q", action)
}
