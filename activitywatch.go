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
	"strconv"
	"strings"
	"time"
)

type Record struct {
	Bucket string         `json:"bucket"`
	Type   string         `json:"type"`
	Data   map[string]any `json:"data"`
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
func baseData(u Update, p Pane) map[string]any {
	project := ""
	for _, w := range u.Snapshot.Workspaces {
		if w.ID == p.Workspace {
			project = w.Label
			break
		}
	}
	return map[string]any{"machine_id": u.Machine.ID, "machine": u.Machine.Label, "session": u.Machine.Session, "workspace_id": p.Workspace, "project": project, "tab_id": p.Tab, "pane_id": p.ID, "terminal_id": p.Terminal, "cwd": p.CWD, "foreground_cwd": p.ForegroundCWD, "connection_epoch": u.Epoch}
}
func agentRecords(host string, u Update) []Record {
	var records []Record
	for _, p := range u.Snapshot.Panes {
		if p.Agent == "" {
			continue
		}
		d := baseData(u, p)
		d["agent"] = p.Agent
		d["status"] = p.Status
		terminal := p.Terminal
		if terminal == "" {
			terminal = p.ID
		}
		records = append(records, Record{"aw-watcher-herdr-agent_" + identity(host, u.Machine.ID, u.Machine.Session, terminal), "herdr.agent.status", d})
	}
	return records
}
func focusRecord(host string, u Update, epoch string) (Record, bool) {
	for _, p := range u.Snapshot.Panes {
		if p.ID == u.Snapshot.FocusedPane {
			d := baseData(u, p)
			d["scope"] = "machine-selection-and-server-focus"
			d["selection_epoch"] = epoch
			return Record{"aw-watcher-herdr-focus_" + identity(host), "herdr.focus", d}, true
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
			err = w.post(ctx, path, map[string]any{"client": "aw-watcher-herdr", "type": r.Type, "hostname": w.config.Host}, true)
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
