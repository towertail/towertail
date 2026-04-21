package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestRunVersion(t *testing.T) {
	var out, errb bytes.Buffer
	code := run([]string{"--version"}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errb.String())
	}
	if !strings.HasPrefix(out.String(), "towertail-agent ") {
		t.Errorf("unexpected version output: %q", out.String())
	}
}

func TestRunSelfCheck(t *testing.T) {
	var out, errb bytes.Buffer
	code := run([]string{"--self-check"}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errb.String())
	}
	if strings.TrimSpace(out.String()) != "ok" {
		t.Errorf("expected 'ok', got %q", out.String())
	}
}

func TestRunOnceEmitsValidSample(t *testing.T) {
	var out, errb bytes.Buffer
	code := run([]string{"--once"}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errb.String())
	}
	var m map[string]any
	if err := json.Unmarshal(out.Bytes(), &m); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, out.String())
	}
	if v, ok := m["v"].(float64); !ok || int(v) != 1 {
		t.Errorf("v != 1: %v", m["v"])
	}
	for _, k := range []string{"ts", "host", "cpu", "mem", "swap", "errors"} {
		if _, ok := m[k]; !ok {
			t.Errorf("missing key %q", k)
		}
	}
}

func TestRunOnceNoDiskNoNetOmitsFields(t *testing.T) {
	var out, errb bytes.Buffer
	code := run([]string{"--once", "--no-disk", "--no-net"}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errb.String())
	}
	var m map[string]any
	if err := json.Unmarshal(out.Bytes(), &m); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if _, ok := m["disks"]; ok {
		t.Error("disks should be omitted with --no-disk")
	}
	if _, ok := m["net"]; ok {
		t.Error("net should be omitted with --no-net")
	}
}

func TestRunDefaultsToOnce(t *testing.T) {
	// Spec §3.1: one-shot is the default for v1.
	var out, errb bytes.Buffer
	code := run([]string{}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errb.String())
	}
	var m map[string]any
	if err := json.Unmarshal(out.Bytes(), &m); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
}
