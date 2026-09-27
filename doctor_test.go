package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDoctorAllChecksPass(t *testing.T) {
	// Mock Herdr socket via fixture
	f := fixture(t)

	// Mock Herdr CLI executable
	herdrBin := filepath.Join(t.TempDir(), "herdr")
	if err := os.WriteFile(herdrBin, []byte("#!/bin/sh\necho 'herdr 0.9.0'\n"), 0700); err != nil {
		t.Fatal(err)
	}

	// Mock ActivityWatch server
	awServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/0/info" {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"version": "v0.12.2", "hostname": "test-host"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer awServer.Close()

	c := testConfig()
	c.Herdr = herdrBin
	c.Socket = f.path
	c.AW = awServer.URL
	c.Timeout = time.Second

	var out bytes.Buffer
	err := runDoctor(context.Background(), c, &out)
	if err != nil {
		t.Fatalf("expected doctor to pass, got error: %v, output:\n%s", err, out.String())
	}
	output := out.String()
	if !strings.Contains(output, "[✓] Herdr CLI") {
		t.Errorf("missing CLI success: %s", output)
	}
	if !strings.Contains(output, "[✓] Herdr socket") {
		t.Errorf("missing socket success: %s", output)
	}
	if !strings.Contains(output, "[✓] ActivityWatch API") {
		t.Errorf("missing AW success: %s", output)
	}
	if !strings.Contains(output, "All checks passed") {
		t.Errorf("missing success summary: %s", output)
	}
}

func TestDoctorDetectsFailures(t *testing.T) {
	c := testConfig()
	c.Herdr = filepath.Join(t.TempDir(), "nonexistent-herdr")
	c.Socket = filepath.Join(t.TempDir(), "nonexistent.sock")
	c.AW = "http://127.0.0.1:1" // unreachable port
	c.Timeout = 100 * time.Millisecond

	var out bytes.Buffer
	err := runDoctor(context.Background(), c, &out)
	if err == nil {
		t.Fatal("expected doctor to fail on missing components")
	}
	output := out.String()
	if !strings.Contains(output, "[✗] Herdr CLI") {
		t.Errorf("expected Herdr CLI failure in output: %s", output)
	}
	if !strings.Contains(output, "[✗] Herdr socket") {
		t.Errorf("expected socket failure in output: %s", output)
	}
	if !strings.Contains(output, "[✗] ActivityWatch API") {
		t.Errorf("expected AW failure in output: %s", output)
	}
	if !strings.Contains(output, "Issues detected") {
		t.Errorf("expected failure summary in output: %s", output)
	}
}
