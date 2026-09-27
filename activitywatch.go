package main

import (
	"bytes"
	"context"
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

type FocusedInfo struct {
	WorkspaceID    string `json:"workspace_id"`
	TabID          string `json:"tab_id"`
	PaneID         string `json:"pane_id"`
	TerminalID     string `json:"terminal_id"`
	Project        string `json:"project"`
	CWD            string `json:"cwd"`
	ForegroundCWD  string `json:"foreground_cwd"`
	Agent          string `json:"agent,omitempty"`
	Status         string `json:"status,omitempty"`
	ExecutionMode  string `json:"execution_mode,omitempty"`
	SelectionEpoch string `json:"selection_epoch,omitempty"`
	Scope          string `json:"scope,omitempty"`
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

func statusRecord(host string, u Update, localSelected bool, focusEpoch string) (Record, bool) {
	if len(u.Snapshot.Panes) == 0 {
		return Record{}, false
	}

	var focusedInfo *FocusedInfo
	var focusedPaneID string
	if localSelected && u.Snapshot.FocusedPane != "" {
		for _, p := range u.Snapshot.Panes {
			if p.ID == u.Snapshot.FocusedPane {
				focusedPaneID = p.ID
				terminal := p.Terminal
				if terminal == "" {
					terminal = p.ID
				}
				execMode := ""
				if p.Agent != "" {
					execMode = "supervised"
				}
				focusedInfo = &FocusedInfo{
					WorkspaceID:    p.Workspace,
					TabID:          p.Tab,
					PaneID:         p.ID,
					TerminalID:     terminal,
					Project:        resolveProject(u, p),
					CWD:            p.CWD,
					ForegroundCWD:  p.ForegroundCWD,
					Agent:          p.Agent,
					Status:         p.Status,
					ExecutionMode:  execMode,
					SelectionEpoch: focusEpoch,
					Scope:          "machine-selection-and-server-focus",
				}
				break
			}
		}
	}

	agents := make([]AgentInfo, 0)
	for _, p := range u.Snapshot.Panes {
		if p.Agent == "" {
			continue
		}
		isFocused := (localSelected && p.ID == focusedPaneID)
		execMode := "autonomous"
		if isFocused {
			execMode = "supervised"
		}
		isTerminal := (p.Status == "done" || p.Status == "exited" || p.Status == "error")

		terminal := p.Terminal
		if terminal == "" {
			terminal = p.ID
		}

		agents = append(agents, AgentInfo{
			Agent:         p.Agent,
			Status:        p.Status,
			Project:       resolveProject(u, p),
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

	// Deterministic sorting by PaneID to ensure reproducible JSON serialization.
	sort.Slice(agents, func(i, j int) bool {
		return agents[i].PaneID < agents[j].PaneID
	})

	workingCount := 0
	blockedCount := 0
	for _, it := range agents {
		if it.Status == "working" {
			workingCount++
		} else if it.Status == "blocked" {
			blockedCount++
		}
	}

	dominantState := "idle"
	if workingCount > 0 {
		dominantState = "working"
	} else if blockedCount > 0 {
		dominantState = "blocked"
	}

	interaction := "manual"
	if focusedInfo != nil && focusedInfo.Agent != "" {
		interaction = "supervised"
	} else if workingCount > 0 || blockedCount > 0 {
		interaction = "autonomous"
	}

	primaryProject := "default"
	if focusedInfo != nil && focusedInfo.Project != "" && focusedInfo.Project != "default" {
		primaryProject = focusedInfo.Project
	} else if len(agents) > 0 {
		for _, a := range agents {
			if a.Status == "working" && a.Project != "" && a.Project != "default" {
				primaryProject = a.Project
				break
			}
		}
		if primaryProject == "default" && agents[0].Project != "" {
			primaryProject = agents[0].Project
		}
	}

	primaryAgent := ""
	if focusedInfo != nil && focusedInfo.Agent != "" {
		primaryAgent = focusedInfo.Agent
	} else if len(agents) > 0 {
		for _, a := range agents {
			if a.Status == "working" {
				primaryAgent = a.Agent
				break
			}
		}
		if primaryAgent == "" {
			primaryAgent = agents[0].Agent
		}
	}

	title := fmt.Sprintf("[%s] %s (%s)", primaryProject, dominantState, interaction)

	var focusedData any
	if focusedInfo != nil {
		focusedData = focusedInfo
	}

	d := map[string]any{
		"app":              "Herdr",
		"title":            title,
		"project":          primaryProject,
		"primary_project":  primaryProject,
		"primary_agent":    primaryAgent,
		"dominant_state":   dominantState,
		"interaction":      interaction,
		"machine_id":       u.Machine.ID,
		"machine":          u.Machine.Label,
		"session":          u.Machine.Session,
		"active_count":     len(agents),
		"working_count":    workingCount,
		"blocked_count":    blockedCount,
		"agents":           agents,
		"focused":          focusedData,
		"connection_epoch": u.Epoch,
	}

	bucket := "aw-watcher-herdr_" + host
	name := fmt.Sprintf("Herdr (%s)", host)
	return Record{
		Bucket: bucket,
		Type:   "herdr.status",
		Data:   d,
		Name:   name,
	}, true
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
