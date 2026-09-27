package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testConfig() Config {
	return Config{Host: "test", HeartbeatInterval: time.Second, PulseTime: time.Second, Timeout: time.Second, RetryInterval: time.Millisecond, SelectionInterval: time.Second}
}
func TestHeartbeatAndFailureGap(t *testing.T) {
	// Durations here are synthetic test inputs, not runtime defaults.
	var events []Event
	creates := 0
	fail := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("method %s", r.Method)
		}
		if strings.HasSuffix(r.URL.Path, "/heartbeat") {
			if r.URL.Query().Get("pulsetime") != "1" {
				t.Error("wrong pulsetime")
			}
			var e Event
			if err := json.NewDecoder(r.Body).Decode(&e); err != nil {
				t.Error(err)
			}
			if fail {
				w.WriteHeader(503)
				return
			}
			events = append(events, e)
		} else {
			creates++
			w.WriteHeader(304)
		}
	}))
	defer server.Close()
	c := testConfig()
	c.AW = server.URL
	writer := newWriter(c, nil)
	r := Record{"bucket", "herdr.focus", map[string]any{"project": "fixture"}}
	now := time.Unix(0, 0)
	if err := writer.Emit(context.Background(), r, now); err != nil {
		t.Fatal(err)
	}
	if err := writer.Emit(context.Background(), r, now); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatal("unchanged event was not suppressed")
	}
	fail = true
	if err := writer.Emit(context.Background(), r, now.Add(time.Second)); err == nil {
		t.Fatal("expected HTTP error")
	}
	fail = false
	if err := writer.Emit(context.Background(), r, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || creates != 2 {
		t.Fatalf("events=%d creates=%d", len(events), creates)
	}
	if events[0].Data["delivery_epoch"] == events[1].Data["delivery_epoch"] {
		t.Fatal("failure gap would be merged")
	}
	if events[0].Duration != 0 {
		t.Fatal("heartbeat must not predict future duration")
	}
}
func TestIndependentAgentBucketsAndFocusScope(t *testing.T) {
	u := Update{Machine: Machine{ID: "m", Label: "remote", Session: "default"}, Epoch: "connected", Snapshot: Snapshot{FocusedPane: "w1:p1", Panes: []Pane{{ID: "w1:p1", Terminal: "t1", Agent: "codex", Status: "working"}, {ID: "w1:p2", Terminal: "t2", Agent: "codex", Status: "idle"}}}}
	records := agentRecords("host", u)
	if len(records) != 2 || records[0].Bucket == records[1].Bucket {
		t.Fatal("parallel agents share a bucket")
	}
	other := u
	other.Machine.ID = "other"
	if agentRecords("host", other)[0].Bucket == records[0].Bucket {
		t.Fatal("machines share a bucket")
	}
	focus, ok := focusRecord("host", u, "selection")
	if !ok || focus.Data["scope"] != "machine-selection-and-server-focus" {
		t.Fatal("wrong focus scope")
	}
	if _, ok := focus.Data["agent"]; ok {
		t.Fatal("agent transitions should not fragment focus")
	}
}
func TestConfigurationRequiresExplicitDurations(t *testing.T) {
	if _, err := parseConfig(nil); err == nil {
		t.Fatal("missing intervals accepted")
	}
	args := []string{"--selection-interval=1s", "--heartbeat-interval=1s", "--pulsetime=1s", "--retry-interval=1s", "--timeout=1s"}
	if _, err := parseConfig(args); err != nil {
		t.Fatal(err)
	}
	if _, err := parseConfig(append(args, "--pulsetime=1ms")); err == nil {
		t.Fatal("merge window shorter than heartbeat accepted")
	}
}

func TestOldSourceTimeDoesNotExtendRecord(t *testing.T) {
	var output strings.Builder
	c := testConfig()
	c.DryRun = true
	writer := newWriter(c, &output)
	r := Record{"bucket", "herdr.agent.status", map[string]any{"status": "working"}}
	now := time.Unix(10, 0)
	if err := writer.Emit(context.Background(), r, now); err != nil {
		t.Fatal(err)
	}
	// A catalog refresh reuses this timestamp rather than fabricating new source activity.
	if err := writer.Emit(context.Background(), r, now); err != nil {
		t.Fatal(err)
	}
	r.Data["status"] = "idle"
	if err := writer.Emit(context.Background(), r, now.Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if strings.Count(output.String(), "\n") != 1 {
		t.Fatal("stale observation wrote a new record")
	}
}
