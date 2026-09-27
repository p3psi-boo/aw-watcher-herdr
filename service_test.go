package main

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestServicePaths(t *testing.T) {
	for _, tc := range []struct{ platform, home, xdg, want string }{
		{"darwin", "/Users/example", "/custom", "/Users/example/Library/LaunchAgents/org.activitywatch.herdr.plist"},
		{"linux", "/home/example", "", "/home/example/.config/systemd/user/aw-watcher-herdr.service"},
		{"linux", "/home/example", "/custom", "/custom/systemd/user/aw-watcher-herdr.service"},
	} {
		s, err := servicePaths(tc.platform, tc.home, tc.xdg, 501)
		if err != nil {
			t.Fatal(err)
		}
		if s.config != tc.want {
			t.Fatalf("got %s want %s", s.config, tc.want)
		}
	}
	if _, err := servicePaths("windows", "/home", "", 1); err == nil {
		t.Fatal("unsupported platform accepted")
	}
	if _, err := servicePaths("linux", "/home", "relative", 1); err == nil {
		t.Fatal("relative config directory accepted")
	}
}

func TestServiceRenderingPreservesArguments(t *testing.T) {
	c := testConfig()
	c.Herdr = `/Applications/Herdr & "Tools"/herdr`
	c.Socket = `/Users/test space/会话%/$HOME.sock`
	c.AW = "http://localhost:5600"
	args := serviceArguments(`/Users/test space/bin/watcher`, c)
	env := map[string]string{"HOME": "/Users/test space", "HERDR_SOCKET_PATH": c.Socket}
	s, _ := servicePaths("darwin", "/Users/test space", "", 501)
	data, err := renderService(s, args, env, c.RetryInterval.Seconds())
	if err != nil {
		t.Fatal(err)
	}
	dec := xml.NewDecoder(bytes.NewReader(data))
	var got []string
	inArray := false
	for {
		token, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		switch token := token.(type) {
		case xml.StartElement:
			if token.Name.Local == "array" {
				inArray = true
			}
			if token.Name.Local == "string" && inArray {
				var value string
				if err := dec.DecodeElement(&value, &token); err != nil {
					t.Fatal(err)
				}
				got = append(got, value)
			}
		case xml.EndElement:
			if token.Name.Local == "array" {
				inArray = false
			}
		}
	}
	if !reflect.DeepEqual(got, args) {
		t.Fatalf("arguments changed: %q != %q", got, args)
	}
	parsed, err := parseConfig(args[1:])
	if err != nil {
		t.Fatal(err)
	}
	if parsed != c {
		t.Fatalf("saved config differs: %+v != %+v", parsed, c)
	}
	s, _ = servicePaths("linux", "/home/test", "", 501)
	data, err = renderService(s, args, env, c.RetryInterval.Seconds())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"/Users/test space/bin/watcher"`, `会话%%/$$HOME.sock`, `Environment="HERDR_SOCKET_PATH=/Users/test space/会话%%/$HOME.sock"`, "StandardError=journal", "WantedBy=default.target"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("missing %q in %s", want, data)
		}
	}
	if got := unitQuote("a\"b\\c\nd\t%$", true); got != `"a\"b\\c\nd\t%%$$"` {
		t.Fatalf("escaping: %q", got)
	}
}

type fakeServiceControl struct {
	calls     []string
	loaded    bool
	failStart bool
}

func (f *fakeServiceControl) run(_ context.Context, name string, args ...string) ([]byte, error) {
	call := name + " " + strings.Join(args, " ")
	f.calls = append(f.calls, call)
	switch {
	case strings.Contains(call, "--property=LoadState"):
		if f.loaded {
			return []byte("loaded\n"), nil
		}
		return []byte("not-found\n"), nil
	case strings.HasPrefix(call, "launchctl print") && strings.HasSuffix(call, "/"+serviceLabel):
		if !f.loaded {
			return nil, errors.New("not loaded")
		}
	case strings.Contains(call, "bootstrap") || strings.Contains(call, "enable --now") || strings.Contains(call, "kickstart") || strings.Contains(call, "restart"):
		if f.failStart {
			return nil, errors.New("start failed")
		}
		f.loaded = true
	case strings.Contains(call, "bootout") || strings.Contains(call, "disable --now"):
		f.loaded = false
	}
	return nil, nil
}

func serviceFixture(t *testing.T, platform string) (serviceManager, *fakeServiceControl, []byte) {
	t.Helper()
	s, err := servicePaths(platform, t.TempDir(), "", 501)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeServiceControl{}
	m := serviceManager{s, f.run, io.Discard}
	data, err := renderService(s, []string{"/path/to/watcher"}, nil, testConfig().RetryInterval.Seconds())
	if err != nil {
		t.Fatal(err)
	}
	return m, f, data
}

func TestServiceLifecycle(t *testing.T) {
	for _, platform := range []string{"darwin", "linux"} {
		t.Run(platform, func(t *testing.T) {
			m, f, data := serviceFixture(t, platform)
			ctx := context.Background()
			if err := m.install(ctx, data); err != nil {
				t.Fatal(err)
			}
			if !f.loaded {
				t.Fatal("service was not started")
			}
			saved, err := m.ownedConfig()
			if err != nil || !bytes.Equal(saved, data) {
				t.Fatalf("saved config: %s %v", saved, err)
			}
			before := len(f.calls)
			if err := m.install(ctx, data); err != nil {
				t.Fatal(err)
			}
			if len(f.calls) != before {
				t.Fatal("identical installation changed running service")
			}
			if err := m.install(ctx, append(append([]byte{}, data...), '\n')); err == nil {
				t.Fatal("changed configuration replaced existing service")
			}
			if err := m.manage(ctx, "status"); err != nil {
				t.Fatal(err)
			}
			if err := m.manage(ctx, "stop"); err != nil {
				t.Fatal(err)
			}
			if f.loaded {
				t.Fatal("service still loaded")
			}
			if err := m.manage(ctx, "start"); err != nil {
				t.Fatal(err)
			}
			if !f.loaded {
				t.Fatal("service did not restart")
			}
			if err := m.manage(ctx, "restart"); err != nil {
				t.Fatal(err)
			}
			if !f.loaded {
				t.Fatal("service not loaded after restart")
			}
			if err := m.manage(ctx, "uninstall"); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(m.layout.config); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("configuration was not removed")
			}
			if f.loaded {
				t.Fatal("uninstall left service running")
			}
			if platform == "darwin" {
				if _, err := os.Stat(m.layout.logDir); err != nil {
					t.Fatal("log directory was removed")
				}
			}
		})
	}
}

func TestServiceProtectsExternalConfigurations(t *testing.T) {
	for _, kind := range []string{"file", "symlink", "loaded"} {
		for _, platform := range []string{"darwin", "linux"} {
			t.Run(platform+"/"+kind, func(t *testing.T) {
				m, f, data := serviceFixture(t, platform)
				if kind == "loaded" {
					f.loaded = true
				} else {
					if err := os.MkdirAll(filepath.Dir(m.layout.config), 0700); err != nil {
						t.Fatal(err)
					}
					if kind == "file" {
						if err := os.WriteFile(m.layout.config, []byte("external config"), 0600); err != nil {
							t.Fatal(err)
						}
					} else {
						target := filepath.Join(t.TempDir(), "external")
						if err := os.WriteFile(target, data, 0600); err != nil {
							t.Fatal(err)
						}
						if err := os.Symlink(target, m.layout.config); err != nil {
							t.Fatal(err)
						}
					}
				}
				if err := m.install(context.Background(), data); err == nil {
					t.Fatal("external service accepted")
				}
				before := len(f.calls)
				for _, action := range []string{"start", "stop", "uninstall"} {
					if err := m.manage(context.Background(), action); err == nil {
						t.Fatalf("external service %s accepted", action)
					}
				}
				if len(f.calls) != before {
					t.Fatal("external service was controlled")
				}
				if kind != "loaded" {
					if _, err := os.Lstat(m.layout.config); err != nil {
						t.Fatal("external configuration removed")
					}
				}
			})
		}
	}
}

func TestServiceFailedStartCanBeRetried(t *testing.T) {
	for _, platform := range []string{"darwin", "linux"} {
		t.Run(platform, func(t *testing.T) {
			m, f, data := serviceFixture(t, platform)
			f.failStart = true
			if err := m.install(context.Background(), data); err == nil || !strings.Contains(err.Error(), "service start") {
				t.Fatalf("missing recovery instructions: %v", err)
			}
			if _, err := m.ownedConfig(); err != nil {
				t.Fatal(err)
			}
			f.failStart = false
			if err := m.manage(context.Background(), "start"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestServicePreviewDoesNotInstall(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	herdr := filepath.Join(home, "herdr")
	if err := os.WriteFile(herdr, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := serviceCLI(context.Background(), []string{"install", "--dry-run", "--herdr=" + herdr}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), serviceMarker) {
		t.Fatal("missing preview")
	}
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "herdr" {
		t.Fatal("preview wrote installation files")
	}
	for _, args := range [][]string{{"unknown"}, {"stop", "--timeout=1s"}} {
		if err := serviceCLI(context.Background(), args, &out); err == nil {
			t.Fatal("invalid service arguments accepted")
		}
	}
}
