package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Record struct {
	Bucket string         `json:"bucket"`
	Type   string         `json:"type"`
	Data   map[string]any `json:"data"`
	Name   string         `json:"name,omitempty"`
}
type Event struct {
	Timestamp time.Time      `json:"timestamp"`
	Duration  float64        `json:"duration"`
	Data      map[string]any `json:"data"`
}

func identity(parts ...string) string {
	b, _ := json.Marshal(parts)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func resolveProject(u Update, p Pane) string {
	for _, w := range u.Snapshot.Workspaces {
		if w.ID == p.Workspace && w.Label != "" {
			return w.Label
		}
	}
	if p.CWD != "" {
		base := filepath.Base(p.CWD)
		if base != "." && base != "/" {
			return base
		}
	}
	if p.Workspace != "" {
		return p.Workspace
	}
	return "default"
}

func baseData(u Update, p Pane) map[string]any {
	project := resolveProject(u, p)
	title := fmt.Sprintf("[%s]", project)
	if p.CWD != "" {
		base := filepath.Base(p.CWD)
		if base != "." && base != "/" && base != project {
			title = fmt.Sprintf("[%s] %s", project, base)
		}
	}
	return map[string]any{
		"app":              "Herdr",
		"title":            title,
		"machine_id":       u.Machine.ID,
		"machine":          u.Machine.Label,
		"session":          u.Machine.Session,
		"workspace_id":     p.Workspace,
		"project":          project,
		"tab_id":           p.Tab,
		"pane_id":          p.ID,
		"terminal_id":      p.Terminal,
		"cwd":              p.CWD,
		"foreground_cwd":   p.ForegroundCWD,
		"connection_epoch": u.Epoch,
	}
}

type AgentInfo struct {
	Agent         string `json:"agent"`
	Status        string `json:"status"`
	Project       string `json:"project"`
	WorkspaceID   string `json:"workspace_id"`
	TabID         string `json:"tab_id"`
	PaneID        string `json:"pane_id"`
	TerminalID    string `json:"terminal_id"`
	CWD           string `json:"cwd"`
	ForegroundCWD string `json:"foreground_cwd"`
	IsFocused     bool   `json:"is_focused"`
	ExecutionMode string `json:"execution_mode"`
	IsTerminal    bool   `json:"is_terminal"`
}

func agentRecords(host string, u Update) []Record {
	var items []AgentInfo
	for _, p := range u.Snapshot.Panes {
		if p.Agent == "" {
			continue
		}
		project := resolveProject(u, p)
		isFocused := (p.ID == u.Snapshot.FocusedPane)
		execMode := "autonomous"
		if isFocused {
			execMode = "supervised"
		}
		isTerminal := (p.Status == "done" || p.Status == "exited" || p.Status == "error")

		terminal := p.Terminal
		if terminal == "" {
			terminal = p.ID
		}

		items = append(items, AgentInfo{
			Agent:         p.Agent,
			Status:        p.Status,
			Project:       project,
			WorkspaceID:   p.Workspace,
			TabID:         p.Tab,
			PaneID:        p.ID,
			TerminalID:    terminal,
			CWD:           p.CWD,
			ForegroundCWD: p.ForegroundCWD,
			IsFocused:     isFocused,
			ExecutionMode: execMode,
			IsTerminal:    isTerminal,
		})
	}

	if len(items) == 0 {
		return nil
	}

	// Deterministic sorting by PaneID to ensure reproducible JSON serialization.
	sort.Slice(items, func(i, j int) bool {
		return items[i].PaneID < items[j].PaneID
	})

	workingCount := 0
	blockedCount := 0
	for _, it := range items {
		if it.Status == "working" {
			workingCount++
		} else if it.Status == "blocked" {
			blockedCount++
		}
	}

	var title string
	if len(items) == 1 {
		title = fmt.Sprintf("%s: %s [%s]", items[0].Agent, items[0].Status, items[0].Project)
	} else {
		var parts []string
		for _, it := range items {
			parts = append(parts, fmt.Sprintf("%s (%s)", it.Agent, it.Status))
		}
		title = fmt.Sprintf("%d agents: %s", len(items), strings.Join(parts, ", "))
	}

	d := map[string]any{
		"app":              "Herdr",
		"title":            title,
		"machine_id":       u.Machine.ID,
		"machine":          u.Machine.Label,
		"session":          u.Machine.Session,
		"active_count":     len(items),
		"working_count":    workingCount,
		"blocked_count":    blockedCount,
		"agents":           items,
		"connection_epoch": u.Epoch,
	}

	bucket := "aw-watcher-herdr-agent_" + identity(host)
	name := fmt.Sprintf("Herdr Agents (%s)", host)
	return []Record{{
		Bucket: bucket,
		Type:   "herdr.agent.status",
		Data:   d,
		Name:   name,
	}}
}

func focusRecord(host string, u Update, epoch string) (Record, bool) {
	for _, p := range u.Snapshot.Panes {
		if p.ID == u.Snapshot.FocusedPane {
			d := baseData(u, p)
			d["scope"] = "machine-selection-and-server-focus"
			d["selection_epoch"] = epoch
			bucket := "aw-watcher-herdr-focus_" + identity(host)
			name := "Herdr Focus (" + host + ")"
			return Record{
				Bucket: bucket,
				Type:   "herdr.focus",
				Data:   d,
				Name:   name,
			}, true
		}
	}
	return Record{}, false
}

type sent struct {
	signature string
	at        time.Time
}
type Writer struct {
	config  Config
	client  *http.Client
	out     io.Writer
	buckets map[string]bool
	last    map[string]sent
	epochs  map[string]string
}

func newWriter(c Config, out io.Writer) *Writer {
	return &Writer{config: c, client: &http.Client{Timeout: c.Timeout}, out: out, buckets: map[string]bool{}, last: map[string]sent{}, epochs: map[string]string{}}
}
func (w *Writer) post(ctx context.Context, path string, payload any, existingOK bool) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(w.config.AW, "/")+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := w.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if (res.StatusCode >= 200 && res.StatusCode < 300) || (existingOK && res.StatusCode == http.StatusNotModified) {
		_, err = io.Copy(io.Discard, res.Body)
		return err
	}
	return fmt.Errorf("ActivityWatch HTTP %d for %s", res.StatusCode, path)
}
func (w *Writer) Emit(ctx context.Context, r Record, at time.Time) error {
	raw, err := json.Marshal(r.Data)
	if err != nil {
		return err
	}
	prev, ok := w.last[r.Bucket]
	if ok && at.Before(prev.at) {
		return nil
	}
	if ok && prev.signature == string(raw) && at.Sub(prev.at) < w.config.HeartbeatInterval {
		return nil
	}
	if w.epochs[r.Bucket] == "" {
		w.epochs[r.Bucket] = newID()
	}
	data := make(map[string]any, len(r.Data)+1)
	for k, v := range r.Data {
		data[k] = v
	}
	data["delivery_epoch"] = w.epochs[r.Bucket]
	event := Event{Timestamp: at.UTC(), Data: data}
	if w.config.DryRun {
		err = json.NewEncoder(w.out).Encode(struct {
			Record
			Event Event `json:"event"`
		}{r, event})
	} else {
		path := "/api/0/buckets/" + url.PathEscape(r.Bucket)
		// Recheck creation after a failed request; this also handles an AW database reset.
		if !w.buckets[r.Bucket] {
			meta := map[string]any{"client": "aw-watcher-herdr", "type": r.Type, "hostname": w.config.Host}
			if r.Name != "" {
				meta["name"] = r.Name
			}
			err = w.post(ctx, path, meta, true)
			if err == nil {
				w.buckets[r.Bucket] = true
			}
		}
		if err == nil {
			err = w.post(ctx, path+"/heartbeat?pulsetime="+strconv.FormatFloat(w.config.PulseTime.Seconds(), 'f', -1, 64), event, false)
		}
	}
	if err != nil {
		delete(w.last, r.Bucket)
		delete(w.buckets, r.Bucket)
		delete(w.epochs, r.Bucket)
		return err
	}
	w.last[r.Bucket] = sent{string(raw), at}
	return nil
}
