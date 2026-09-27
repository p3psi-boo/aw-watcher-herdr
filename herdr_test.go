package main

import (
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// fakeHerdr mirrors 0.9.0: ordinary requests close after one response, while
// subscriptions acknowledge and then remain open for server-pushed events.
type fakeHerdr struct {
	listener   net.Listener
	path       string
	mu         sync.Mutex
	clients    []net.Conn
	state      Snapshot
	subscribed chan net.Conn
	stop       chan struct{}
	wg         sync.WaitGroup
}

func fixture(t *testing.T) *fakeHerdr {
	t.Helper()
	f := &fakeHerdr{path: filepath.Join(t.TempDir(), "s"), subscribed: make(chan net.Conn), stop: make(chan struct{}), state: Snapshot{FocusedPane: "p", FocusedTab: "t", FocusedWorkspace: "w", Panes: []Pane{{ID: "p", Tab: "t", Workspace: "w", Terminal: "term", Agent: "codex", Status: "idle"}}, Workspaces: []Workspace{{ID: "w", ActiveTab: "t"}}, Layouts: []Layout{{Tab: "t", FocusedPane: "p"}}}}
	var err error
	f.listener, err = net.Listen("unix", f.path)
	if err != nil {
		t.Fatal(err)
	}
	f.wg.Add(1)
	go func() {
		defer f.wg.Done()
		for {
			c, err := f.listener.Accept()
			if err != nil {
				return
			}
			f.mu.Lock()
			f.clients = append(f.clients, c)
			f.mu.Unlock()
			f.wg.Add(1)
			go func() {
				defer f.wg.Done()
				defer c.Close()
				var r struct {
					Method string `json:"method"`
				}
				if json.NewDecoder(c).Decode(&r) != nil {
					return
				}
				switch r.Method {
				case "session.snapshot":
					json.NewEncoder(c).Encode(map[string]any{"result": map[string]any{"snapshot": f.state}})
				case "ping":
					json.NewEncoder(c).Encode(map[string]any{"result": map[string]any{"type": "pong"}})
				case "events.subscribe":
					json.NewEncoder(c).Encode(map[string]any{"result": map[string]any{"type": "subscription_started"}})
					select {
					case f.subscribed <- c:
					case <-f.stop:
						return
					}
					<-f.stop
				}
			}()
		}
	}()
	t.Cleanup(func() {
		close(f.stop)
		f.listener.Close()
		f.mu.Lock()
		for _, c := range f.clients {
			c.Close()
		}
		f.mu.Unlock()
		f.wg.Wait()
	})
	return f
}
func TestStreamPushAndCancellation(t *testing.T) {
	f := fixture(t)
	c := testConfig()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	updates := make(chan Update)
	done := make(chan error)
	go func() { done <- stream(ctx, c, Machine{ID: "local"}, f.path, updates) }()
	conn := <-f.subscribed
	initial := <-updates
	if !initial.Online || initial.Snapshot.Panes[0].Status != "idle" {
		t.Fatal("missing initial snapshot")
	}
	if err := json.NewEncoder(conn).Encode(map[string]any{"event": "pane_agent_status_changed", "data": map[string]any{"pane_id": "p", "agent_status": "working"}}); err != nil {
		t.Fatal(err)
	}
	changed := <-updates
	if changed.Snapshot.Panes[0].Status != "working" {
		t.Fatal("status push not applied")
	}
	if initial.Snapshot.Panes[0].Status != "idle" {
		t.Fatal("previous update mutated")
	}
	cancel()
	<-done
}
func TestStreamDisconnect(t *testing.T) {
	f := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := make(chan Update)
	done := make(chan error)
	go func() { done <- stream(ctx, testConfig(), Machine{ID: "local"}, f.path, out) }()
	conn := <-f.subscribed
	<-out
	conn.Close()
	if err := <-done; err == nil {
		t.Fatal("disconnect ignored")
	}
}
func TestFocusReducerMaintainsTabSelection(t *testing.T) {
	s := Snapshot{Panes: []Pane{{ID: "p1", Workspace: "w", Tab: "t"}, {ID: "p2", Workspace: "w", Tab: "t"}}, Workspaces: []Workspace{{ID: "w", ActiveTab: "t"}}, Layouts: []Layout{{Tab: "t", FocusedPane: "p1"}}}
	for _, e := range []Envelope{{Event: "pane_focused", Data: json.RawMessage(`{"pane_id":"p2"}`)}, {Event: "workspace_focused", Data: json.RawMessage(`{"workspace_id":"w"}`)}} {
		refresh, err := apply(&s, e)
		if err != nil || refresh {
			t.Fatalf("apply: %v %v", refresh, err)
		}
	}
	if s.FocusedPane != "p2" {
		t.Fatal("cached layout lost the latest pane selection")
	}
	if _, err := apply(&s, Envelope{Event: "pane_created", Data: json.RawMessage(`{}`)}); err != errTopology {
		t.Fatal("new pane should renew status subscriptions")
	}
}
func TestRequestError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := request(ctx, "missing", "ping", struct{}{}, time.Second); err == nil {
		t.Fatal("cancellation ignored")
	}
}

func TestSourceReconnectStartsNewEpoch(t *testing.T) {
	f := fixture(t)
	c := testConfig()
	c.Socket = f.path
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := make(chan Update)
	done := make(chan struct{})
	go func() { defer close(done); source(ctx, c, Machine{ID: "local"}, out) }()
	conn := <-f.subscribed
	first := <-out
	conn.Close()
	offline := <-out
	if offline.Online {
		t.Fatal("disconnection did not suspend source")
	}
	<-f.subscribed
	second := <-out
	if !second.Online || first.Epoch == second.Epoch {
		t.Fatal("reconnect reused the old continuity epoch")
	}
	cancel()
	<-done
}
