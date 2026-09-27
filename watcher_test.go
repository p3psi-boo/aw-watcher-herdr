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

func TestRunWritesStatusAndStops(t *testing.T) {
	f := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan Event, 10)
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
			select {
			case events <- e:
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
	e := <-events
	if e.Data["app"] != "Herdr" {
		t.Errorf("expected app Herdr, got %v", e.Data["app"])
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
	// never turned into a source. Local background agents continue reporting as autonomous.
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
	if record.Type != "herdr.status" || record.Data["machine_id"] != "local" {
		t.Fatalf("unexpected record: %+v", record)
	}
	if record.Data["interaction"] != "autonomous" {
		t.Fatalf("expected autonomous interaction when remote is selected, got %v", record.Data["interaction"])
	}
	if record.Data["focused"] != nil {
		t.Fatalf("expected nil focused pane when remote is selected, got %v", record.Data["focused"])
	}
	if strings.Count(output.String(), "\n") != 1 {
		t.Fatal("remote selection emitted extra events")
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
	if !strings.Contains(output.String(), `"interaction":"supervised"`) {
		t.Fatal("local focus did not resume as supervised")
	}
}

func TestRemovedConnectionOptionsAreRejected(t *testing.T) {
	for _, arg := range []string{"--ssh=ssh", "--local-only"} {
		if _, err := parseConfig([]string{arg}); err == nil {
			t.Fatalf("removed option accepted: %s", arg)
		}
	}
}
