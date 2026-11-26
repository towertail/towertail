package clickhouse

import (
	"strings"
	"testing"
)

func TestAICallClip(t *testing.T) {
	c := AICall{Tool: strings.Repeat("t", 300), Input: strings.Repeat("é", 2000), Error: "short"}
	c.Clip()
	if len(c.Tool) != maxAICallTool || c.Error != "short" {
		t.Errorf("tool = %d bytes, error = %q", len(c.Tool), c.Error)
	}
	// 1024 is even, so the cut falls between two characters of 2 bytes.
	if len(c.Input) != 1024 || !strings.HasSuffix(c.Input, "é") {
		t.Errorf("input = %d bytes", len(c.Input))
	}
	c = AICall{Input: "a" + strings.Repeat("é", 1000)}
	c.Clip()
	if len(c.Input) != 1023 {
		t.Errorf("input = %d bytes, want 1023 (cut before a split character)", len(c.Input))
	}
}
