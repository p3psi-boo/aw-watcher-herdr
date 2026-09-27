package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"time"
)

type Machine struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Session  string `json:"session"`
	Enabled  bool   `json:"enabled"`
	Selected bool   `json:"selected"`
}
type Pane struct {
	ID            string `json:"pane_id"`
	Workspace     string `json:"workspace_id"`
	Tab           string `json:"tab_id"`
	Terminal      string `json:"terminal_id"`
	CWD           string `json:"cwd"`
	ForegroundCWD string `json:"foreground_cwd"`
	Agent         string `json:"agent"`
	Status        string `json:"agent_status"`
}
type Workspace struct {
	ID        string `json:"workspace_id"`
	Label     string `json:"label"`
	ActiveTab string `json:"active_tab_id"`
}
type Layout struct {
	Tab         string `json:"tab_id"`
	FocusedPane string `json:"focused_pane_id"`
}
type Snapshot struct {
	FocusedPane      string      `json:"focused_pane_id"`
	FocusedTab       string      `json:"focused_tab_id"`
	FocusedWorkspace string      `json:"focused_workspace_id"`
	Panes            []Pane      `json:"panes"`
	Workspaces       []Workspace `json:"workspaces"`
	Layouts          []Layout    `json:"layouts"`
}
type Envelope struct {
	Event  string          `json:"event"`
	Data   json.RawMessage `json:"data"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (e Envelope) check() error {
	if e.Error != nil {
		return fmt.Errorf("Herdr %s: %s", e.Error.Code, e.Error.Message)
	}
	return nil
}
func dial(ctx context.Context, path string, timeout time.Duration) (net.Conn, error) {
	return (&net.Dialer{Timeout: timeout}).DialContext(ctx, "unix", path)
}
func request(ctx context.Context, path, method string, params any, timeout time.Duration) (Envelope, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := dial(ctx, path, timeout)
	if err != nil {
		return Envelope{}, err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	deadline, _ := ctx.Deadline()
	if err = conn.SetDeadline(deadline); err != nil {
		return Envelope{}, err
	}
	if err = json.NewEncoder(conn).Encode(map[string]any{"id": "watcher", "method": method, "params": params}); err != nil {
		return Envelope{}, err
	}
	var e Envelope
	if err = json.NewDecoder(conn).Decode(&e); err != nil {
		return e, err
	}
	return e, e.check()
}
func snapshot(ctx context.Context, path string, timeout time.Duration) (Snapshot, error) {
	e, err := request(ctx, path, "session.snapshot", struct{}{}, timeout)
	if err != nil {
		return Snapshot{}, err
	}
	var r struct {
		Snapshot *Snapshot `json:"snapshot"`
	}
	if err = json.Unmarshal(e.Result, &r); err != nil {
		return Snapshot{}, err
	}
	if r.Snapshot == nil {
		return Snapshot{}, errors.New("missing session snapshot")
	}
	return *r.Snapshot, nil
}
func subscriptions(s Snapshot) []map[string]string {
	var out []map[string]string
	for _, name := range []string{"workspace.focused", "workspace.updated", "workspace.renamed", "workspace.closed", "tab.focused", "tab.closed", "pane.focused", "pane.updated", "pane.created", "pane.closed", "pane.moved", "pane.exited", "pane.agent_detected", "layout.updated"} {
		out = append(out, map[string]string{"type": name})
	}
	for _, p := range s.Panes {
		out = append(out, map[string]string{"type": "pane.agent_status_changed", "pane_id": p.ID})
	}
	return out
}
func subscribe(ctx context.Context, path string, s Snapshot, timeout time.Duration) (net.Conn, *json.Decoder, error) {
	conn, err := dial(ctx, path, timeout)
	if err != nil {
		return nil, nil, err
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	fail := func(err error) (net.Conn, *json.Decoder, error) { stop(); conn.Close(); return nil, nil, err }
	if err = conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return fail(err)
	}
	if err = json.NewEncoder(conn).Encode(map[string]any{"id": "watcher-sub", "method": "events.subscribe", "params": map[string]any{"subscriptions": subscriptions(s)}}); err != nil {
		return fail(err)
	}
	dec := json.NewDecoder(conn)
	var ack Envelope
	if err = dec.Decode(&ack); err != nil {
		return fail(err)
	}
	if err = ack.check(); err != nil {
		return fail(err)
	}
	var result struct {
		Type string `json:"type"`
	}
	if err = json.Unmarshal(ack.Result, &result); err != nil {
		return fail(err)
	}
	if result.Type != "subscription_started" {
		return fail(fmt.Errorf("unexpected subscription response: %s", ack.Result))
	}
	if err = conn.SetDeadline(time.Time{}); err != nil {
		return fail(err)
	}
	// The caller owns a child context and cancels it when the stream finishes.
	return conn, dec, nil
}
func listMachines(ctx context.Context, c Config) ([]Machine, error) {
	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, c.Herdr, "machine", "list", "--json").Output()
	if err != nil {
		return nil, fmt.Errorf("machine list: %w", err)
	}
	var ms []Machine
	if err = json.Unmarshal(out, &ms); err != nil {
		return nil, err
	}
	ids := map[string]bool{}
	selected := 0
	for _, m := range ms {
		if m.ID == "" || m.ID == "local" || ids[m.ID] {
			return nil, errors.New("invalid or duplicate machine id")
		}
		ids[m.ID] = true
		if m.Selected {
			selected++
			if !m.Enabled {
				return nil, errors.New("selected machine is disabled")
			}
		}
	}
	if selected > 1 {
		return nil, errors.New("multiple machines marked selected")
	}
	return ms, nil
}
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
