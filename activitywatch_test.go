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
	r := Record{Bucket: "bucket", Type: "herdr.focus", Data: map[string]any{"project": "fixture"}}
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
func TestAggregatedAgentBucketAndFocusScope(t *testing.T) {
	u := Update{Machine: Machine{ID: "m", Label: "remote", Session: "default"}, Epoch: "connected", Snapshot: Snapshot{FocusedPane: "w1:p1", Panes: []Pane{{ID: "w1:p1", Terminal: "t1", Agent: "codex", Status: "working"}, {ID: "w1:p2", Terminal: "t2", Agent: "codex", Status: "idle"}}}}
	records := agentRecords("host", u)
	if len(records) != 1 {
		t.Fatalf("expected 1 aggregated agent record, got %d", len(records))
	}
	if records[0].Data["active_count"] != 2 {
		t.Fatalf("expected active_count=2, got %v", records[0].Data["active_count"])
	}
	otherHostRecords := agentRecords("other-host", u)
	if otherHostRecords[0].Bucket == records[0].Bucket {
		t.Fatal("different hosts should not share a bucket")
	}
	focus, ok := focusRecord("host", u, "selection")
	if !ok || focus.Data["scope"] != "machine-selection-and-server-focus" {
		t.Fatal("wrong focus scope")
	}
	if _, ok := focus.Data["agent"]; ok {
		t.Fatal("agent transitions should not fragment focus")
	}
}

func TestEventSchemaAndOrchestration(t *testing.T) {
	u := Update{
		Machine: Machine{ID: "local", Label: "Local", Session: "/path/to/sock"},
		Epoch:   "conn-1",
		Snapshot: Snapshot{
			FocusedPane: "p1",
			Panes: []Pane{
				{ID: "p1", Workspace: "w1", Tab: "t1", CWD: "/home/user/myproject", Terminal: "term1", Agent: "codex", Status: "working"},
				{ID: "p2", Workspace: "w2", Tab: "t2", CWD: "/home/user/docs", Terminal: "term2", Agent: "claude", Status: "done"},
				{ID: "p3", Workspace: "w3", Tab: "t3", CWD: "", Terminal: "term3", Agent: "gemini", Status: "idle"},
			},
			Workspaces: []Workspace{
				{ID: "w1", Label: "awesome-app"},
				{ID: "w2", Label: ""},
				{ID: "w3", Label: ""},
			},
		},
	}

	focus, ok := focusRecord("myhost", u, "epoch-focus")
	if !ok {
		t.Fatal("expected focus record")
	}
	if focus.Name != "Herdr Focus (myhost)" {
		t.Errorf("expected focus bucket name 'Herdr Focus (myhost)', got %q", focus.Name)
	}
	if focus.Data["app"] != "Herdr" {
		t.Errorf("expected app 'Herdr', got %v", focus.Data["app"])
	}
	if focus.Data["project"] != "awesome-app" {
		t.Errorf("expected project 'awesome-app', got %v", focus.Data["project"])
	}
	if focus.Data["title"] != "[awesome-app] myproject" {
		t.Errorf("expected title '[awesome-app] myproject', got %v", focus.Data["title"])
	}

	agents := agentRecords("myhost", u)
	if len(agents) != 1 {
		t.Fatalf("expected 1 aggregated agent record, got %d", len(agents))
	}
	agg := agents[0]
	if agg.Name != "Herdr Agents (myhost)" {
		t.Errorf("unexpected bucket name: %q", agg.Name)
	}
	if agg.Data["active_count"] != 3 {
		t.Errorf("expected active_count 3, got %v", agg.Data["active_count"])
	}
	if agg.Data["working_count"] != 1 {
		t.Errorf("expected working_count 1, got %v", agg.Data["working_count"])
	}
	items := agg.Data["agents"].([]AgentInfo)
	if len(items) != 3 {
		t.Fatalf("expected 3 items in agents, got %d", len(items))
	}

	// p1 is focused, working
	p1 := items[0]
	if p1.IsFocused != true {
		t.Errorf("p1 should be focused")
	}
	if p1.ExecutionMode != "supervised" {
		t.Errorf("p1 execution_mode should be 'supervised', got %v", p1.ExecutionMode)
	}
	if p1.IsTerminal != false {
		t.Errorf("p1 is_terminal should be false, got %v", p1.IsTerminal)
	}
	if p1.Project != "awesome-app" {
		t.Errorf("expected project awesome-app, got %v", p1.Project)
	}

	// p2 is background, done (terminal state)
	p2 := items[1]
	if p2.IsFocused != false {
		t.Errorf("p2 should not be focused")
	}
	if p2.ExecutionMode != "autonomous" {
		t.Errorf("p2 execution_mode should be 'autonomous', got %v", p2.ExecutionMode)
	}
	if p2.IsTerminal != true {
		t.Errorf("p2 is_terminal should be true, got %v", p2.IsTerminal)
	}
	if p2.Project != "docs" {
		t.Errorf("p2 project should fallback to 'docs', got %v", p2.Project)
	}

	// p3 fallback to workspace ID
	p3 := items[2]
	if p3.Project != "w3" {
		t.Errorf("p3 project should fallback to workspace ID 'w3', got %v", p3.Project)
	}

	// Test bucket creation sends Name to ActivityWatch
	var createdBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/heartbeat") {
			json.NewDecoder(r.Body).Decode(&createdBody)
			w.WriteHeader(304)
			return
		}
		w.WriteHeader(200)
	}))
	defer server.Close()

	c := testConfig()
	c.AW = server.URL
	writer := newWriter(c, nil)
	if err := writer.Emit(context.Background(), agg, time.Now()); err != nil {
		t.Fatal(err)
	}
	if createdBody["name"] != "Herdr Agents (myhost)" {
		t.Errorf("expected bucket creation body to include name, got: %v", createdBody)
	}
}
func TestConfigurationDefaults(t *testing.T) {
	c, err := parseConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.SelectionInterval != time.Second || c.HeartbeatInterval != time.Second || c.PulseTime != 2*time.Second || c.RetryInterval != 10*time.Second || c.Timeout != 5*time.Second {
		t.Fatalf("unexpected defaults: %+v", c)
	}
}

func TestConfigurationDurationOverrides(t *testing.T) {
	c, err := parseConfig([]string{"--selection-interval=2s", "--heartbeat-interval=5s", "--retry-interval=3s", "--timeout=4s"})
	if err != nil {
		t.Fatal(err)
	}
	if c.SelectionInterval != 2*time.Second || c.HeartbeatInterval != 5*time.Second || c.PulseTime != 7500*time.Millisecond || c.RetryInterval != 3*time.Second || c.Timeout != 4*time.Second {
		t.Fatalf("unexpected overrides: %+v", c)
	}
	c, err = parseConfig([]string{"--heartbeat-interval=5s", "--pulsetime=6s"})
	if err != nil {
		t.Fatal(err)
	}
	if c.PulseTime != 6*time.Second {
		t.Fatalf("explicit merge window changed: %s", c.PulseTime)
	}
	c, err = parseConfig([]string{"--heartbeat-interval=500ms"})
	if err != nil {
		t.Fatal(err)
	}
	if c.PulseTime != 1500*time.Millisecond {
		t.Fatalf("unexpected derived window: %s", c.PulseTime)
	}
}

func TestConfigurationRejectsInvalidDurations(t *testing.T) {
	for _, flag := range []string{"selection-interval", "heartbeat-interval", "pulsetime", "retry-interval", "timeout"} {
		for _, value := range []string{"0s", "-1s", "invalid"} {
			t.Run(flag+"="+value, func(t *testing.T) {
				if _, err := parseConfig([]string{"--" + flag + "=" + value}); err == nil {
					t.Fatal("invalid duration accepted")
				}
			})
		}
	}
	if _, err := parseConfig([]string{"--heartbeat-interval=5s", "--pulsetime=2s"}); err == nil {
		t.Fatal("merge window shorter than heartbeat accepted")
	}
}

func TestOldSourceTimeDoesNotExtendRecord(t *testing.T) {
	var output strings.Builder
	c := testConfig()
	c.DryRun = true
	writer := newWriter(c, &output)
	r := Record{Bucket: "bucket", Type: "herdr.agent.status", Data: map[string]any{"status": "working"}}
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
