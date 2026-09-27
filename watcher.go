package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"sync"
	"time"
)

type Update struct {
	Machine  Machine
	Epoch    string
	Snapshot Snapshot
	Online   bool
	At       time.Time
}
type Catalog struct {
	Machines []Machine
	Err      error
}

var errTopology = errors.New("pane topology changed; resubscribe")

func newID() string { return rand.Text() }
func send(ctx context.Context, ch chan<- Update, u Update) bool {
	select {
	case ch <- u:
		return true
	case <-ctx.Done():
		return false
	}
}
func samePanes(a, b Snapshot) bool {
	ids := map[string]bool{}
	for _, p := range a.Panes {
		ids[p.ID] = true
	}
	if len(a.Panes) != len(b.Panes) {
		return false
	}
	for _, p := range b.Panes {
		if !ids[p.ID] {
			return false
		}
	}
	return true
}

// apply preserves status transitions directly from the event instead of querying
// a later snapshot that could already contain a different status.
func apply(s *Snapshot, e Envelope) (bool, error) {
	var d struct {
		PaneID      string  `json:"pane_id"`
		WorkspaceID string  `json:"workspace_id"`
		TabID       string  `json:"tab_id"`
		Status      string  `json:"agent_status"`
		Agent       *string `json:"agent"`
		Pane        *Pane   `json:"pane"`
	}
	if err := json.Unmarshal(e.Data, &d); err != nil {
		return false, err
	}
	switch e.Event {
	case "pane_created", "pane_closed", "pane_moved", "pane_exited", "pane_agent_detected":
		return false, errTopology
	case "pane_agent_status_changed":
		for i := range s.Panes {
			if s.Panes[i].ID == d.PaneID {
				s.Panes[i].Status = d.Status
				if d.Agent != nil {
					s.Panes[i].Agent = *d.Agent
				}
				return false, nil
			}
		}
		return false, errTopology
	case "pane_updated":
		if d.Pane == nil {
			return false, errors.New("pane_updated missing pane")
		}
		for i := range s.Panes {
			if s.Panes[i].ID == d.Pane.ID {
				s.Panes[i] = *d.Pane
				return false, nil
			}
		}
		return false, errTopology
	case "pane_focused":
		for _, p := range s.Panes {
			if p.ID == d.PaneID {
				s.FocusedPane = p.ID
				s.FocusedTab = p.Tab
				s.FocusedWorkspace = p.Workspace
				for i := range s.Layouts {
					if s.Layouts[i].Tab == p.Tab {
						s.Layouts[i].FocusedPane = p.ID
					}
				}
				for i := range s.Workspaces {
					if s.Workspaces[i].ID == p.Workspace {
						s.Workspaces[i].ActiveTab = p.Tab
					}
				}
				return false, nil
			}
		}
		return true, nil
	case "tab_focused":
		s.FocusedTab = d.TabID
		s.FocusedWorkspace = d.WorkspaceID
		for i := range s.Workspaces {
			if s.Workspaces[i].ID == d.WorkspaceID {
				s.Workspaces[i].ActiveTab = d.TabID
			}
		}
		for _, l := range s.Layouts {
			if l.Tab == d.TabID {
				s.FocusedPane = l.FocusedPane
				return false, nil
			}
		}
		return true, nil
	case "workspace_focused":
		for _, w := range s.Workspaces {
			if w.ID == d.WorkspaceID {
				for _, l := range s.Layouts {
					if l.Tab == w.ActiveTab {
						s.FocusedWorkspace = w.ID
						s.FocusedTab = w.ActiveTab
						s.FocusedPane = l.FocusedPane
						return false, nil
					}
				}
			}
		}
		return true, nil
	default:
		return true, nil
	}
}
func stream(ctx context.Context, c Config, m Machine, path string, out chan<- Update) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	initial, err := snapshot(ctx, path, c.Timeout)
	if err != nil {
		return err
	}
	conn, dec, err := subscribe(ctx, path, initial, c.Timeout)
	if err != nil {
		return err
	}
	defer conn.Close()
	// Decode immediately so events produced during the final snapshot stay ordered.
	type decoded struct {
		event Envelope
		err   error
	}
	incoming := make(chan decoded)
	var reader sync.WaitGroup
	reader.Add(1)
	go func() {
		defer reader.Done()
		for {
			var e Envelope
			err := dec.Decode(&e)
			select {
			case incoming <- decoded{e, err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	defer func() { cancel(); conn.Close(); reader.Wait() }()
	state, err := snapshot(ctx, path, c.Timeout)
	if err != nil {
		return err
	}
	if !samePanes(initial, state) {
		return errTopology
	}
	epoch := newID()
	publish := func() bool {
		// Slices must not be shared with the event reducer after delivery.
		copyState := state
		copyState.Panes = append([]Pane(nil), state.Panes...)
		copyState.Workspaces = append([]Workspace(nil), state.Workspaces...)
		copyState.Layouts = append([]Layout(nil), state.Layouts...)
		return send(ctx, out, Update{Machine: m, Epoch: epoch, Snapshot: copyState, Online: true, At: time.Now().UTC()})
	}
	if !publish() {
		return ctx.Err()
	}
	health := time.NewTicker(c.HeartbeatInterval)
	defer health.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case item := <-incoming:
			if item.err != nil {
				return item.err
			}
			if err = item.event.check(); err != nil {
				return err
			}
			if item.event.Event == "" {
				continue
			}
			refresh, err := apply(&state, item.event)
			if err != nil {
				return err
			}
			if refresh {
				state, err = snapshot(ctx, path, c.Timeout)
				if err != nil {
					return err
				}
				if !samePanes(initial, state) {
					return errTopology
				}
			}
			if !publish() {
				return ctx.Err()
			}
		case <-health.C:
			if _, err = request(ctx, path, "ping", struct{}{}, c.Timeout); err != nil {
				return err
			}
			// Refresh optional cwd and titles only at the caller-configured interval.
			state, err = snapshot(ctx, path, c.Timeout)
			if err != nil {
				return err
			}
			if !samePanes(initial, state) {
				return errTopology
			}
			if !publish() {
				return ctx.Err()
			}
		}
	}
}
func source(ctx context.Context, c Config, m Machine, out chan<- Update) {
	for ctx.Err() == nil {
		err := stream(ctx, c, m, c.Socket, out)
		if ctx.Err() != nil {
			return
		}
		send(ctx, out, Update{Machine: m, At: time.Now().UTC()})
		if errors.Is(err, errTopology) {
			continue
		}
		slog.Warn("Herdr source disconnected", "machine", m.Label, "error", err)
		if !sleep(ctx, c.RetryInterval) {
			return
		}
	}
}
func catalog(ctx context.Context, c Config, out chan<- Catalog) {
	for ctx.Err() == nil {
		ms, err := listMachines(ctx, c)
		select {
		case out <- Catalog{ms, err}:
		case <-ctx.Done():
			return
		}
		if !sleep(ctx, c.SelectionInterval) {
			return
		}
	}
}

// selectionLocal is false for a remote selection or an unsuccessful catalog read.
// It never initiates a connection to a machine from the catalog.
func selectionLocal(result Catalog) bool {
	if result.Err != nil {
		return false
	}
	for _, m := range result.Machines {
		if m.Selected {
			return false
		}
	}
	return true
}

type collector struct {
	state         Update
	localSelected bool
	focusEpoch    string
	selectionAt   time.Time
	writer        *Writer
}

func (p *collector) selectMachine(result Catalog, at time.Time) {
	local := selectionLocal(result)
	if local != p.localSelected {
		p.focusEpoch = newID()
		p.selectionAt = at
	}
	p.localSelected = local
}
func (p *collector) publish(ctx context.Context) {
	u := p.state
	if !u.Online {
		return
	}
	for _, r := range agentRecords(p.writer.config.Host, u) {
		if err := p.writer.Emit(ctx, r, u.At); err != nil {
			slog.Warn("ActivityWatch write failed", "bucket", r.Bucket, "error", err)
		}
	}
	if !p.localSelected {
		return
	}
	if r, ok := focusRecord(p.writer.config.Host, u, p.focusEpoch); ok {
		if err := p.writer.Emit(ctx, r, maxTime(u.At, p.selectionAt)); err != nil {
			slog.Warn("ActivityWatch write failed", "bucket", r.Bucket, "error", err)
		}
	}
}
func run(ctx context.Context, c Config) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	updates := make(chan Update)
	catalogs := make(chan Catalog)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		source(ctx, c, Machine{ID: "local", Label: "Local", Session: c.Socket, Enabled: true}, updates)
	}()
	go func() { defer wg.Done(); catalog(ctx, c, catalogs) }()
	defer func() { cancel(); wg.Wait() }()
	p := collector{writer: newWriter(c, os.Stdout), focusEpoch: newID()}
	for {
		select {
		case <-ctx.Done():
			return nil
		case result := <-catalogs:
			if result.Err != nil {
				slog.Warn("machine selection query failed; focus recording suspended", "error", result.Err)
			}
			p.selectMachine(result, time.Now().UTC())
			p.publish(ctx)
		case u := <-updates:
			if !u.Online {
				p.focusEpoch = newID()
			}
			p.state = u
			p.publish(ctx)
		}
	}
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
