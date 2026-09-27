package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunWritesAgentAndFocusAndStops(t *testing.T) {
	f := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	types := make(chan string)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/heartbeat") {
			var e Event
			if err := json.NewDecoder(r.Body).Decode(&e); err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			if e.Data["machine_id"] != "local" {
				t.Errorf("unexpected source %v", e.Data)
			}
			kind := "agent"
			if _, ok := e.Data["scope"]; ok {
				kind = "focus"
			}
			select {
			case types <- kind:
			case <-ctx.Done():
			}
		}
	}))
	defer server.Close()
	c := testConfig()
	c.Socket = f.path
	c.Herdr = filepath.Join(t.TempDir(), "herdr")
	if err := os.WriteFile(c.Herdr, []byte("#!/bin/sh\nprintf '[]\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	c.AW = server.URL
	done := make(chan error)
	go func() { done <- run(ctx, c) }()
	<-f.subscribed
	found := map[string]bool{}
	for len(found) < 2 {
		found[<-types] = true
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestRemoteSelectionSkipsFocusAndResumeStartsNewInterval(t *testing.T) {
	var output strings.Builder
	c := testConfig()
	c.DryRun = true
	now := time.Unix(10, 0)
	p := collector{writer: newWriter(c, &output), focusEpoch: "initial", state: Update{Online: true, At: now, Epoch: "connection", Machine: Machine{ID: "local"}, Snapshot: Snapshot{FocusedPane: "p", Panes: []Pane{{ID: "p", Agent: "codex", Status: "working", Terminal: "t"}}}}}
	p.selectMachine(Catalog{}, now)
	p.publish(context.Background())
	firstEpoch := p.focusEpoch
	output.Reset()
	// A saved remote machine is metadata only: it suppresses local focus and is
	// never turned into a source. Local background agents continue reporting.
	p.selectMachine(Catalog{Machines: []Machine{{ID: "remote", Enabled: true, Selected: true}}}, now.Add(time.Second))
	p.state.At = now.Add(time.Second)
	p.publish(context.Background())
	dec := json.NewDecoder(strings.NewReader(output.String()))
	var record struct {
		Record
		Event Event `json:"event"`
	}
	if err := dec.Decode(&record); err != nil {
		t.Fatal(err)
	}
	if record.Type != "herdr.agent.status" || record.Data["machine_id"] != "local" {
		t.Fatalf("unexpected record: %+v", record)
	}
	if strings.Count(output.String(), "\n") != 1 {
		t.Fatal("remote selection emitted local focus")
	}
	p.selectMachine(Catalog{Err: errors.New("catalog unavailable")}, now.Add(2*time.Second))
	if p.localSelected {
		t.Fatal("failed selection query treated as local")
	}
	output.Reset()
	p.selectMachine(Catalog{}, now.Add(3*time.Second))
	p.state.At = now.Add(3 * time.Second)
	p.publish(context.Background())
	if p.focusEpoch == firstEpoch {
		t.Fatal("returning from remote selection reused the old interval")
	}
	if !strings.Contains(output.String(), `"type":"herdr.focus"`) {
		t.Fatal("local focus did not resume")
	}
}

func TestRemovedConnectionOptionsAreRejected(t *testing.T) {
	for _, arg := range []string{"--ssh=ssh", "--local-only"} {
		if _, err := parseConfig([]string{arg}); err == nil {
			t.Fatalf("removed option accepted: %s", arg)
		}
	}
}
