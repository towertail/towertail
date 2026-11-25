package wire

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestProcessKeyRoundTrip(t *testing.T) {
	for _, name := range []string{"node", "python3.11", "a/b", "my app", "日本語", "x.y.z", strings.Repeat("n", 300)} {
		k := NewProcessKey(name, "/usr/bin/"+name+" --flag")
		s := k.String()
		if strings.Contains(s, "/") || strings.Count(s, ".") < 1 {
			t.Errorf("key %q is not URL safe", s)
		}
		got, err := ParseProcessKey(s)
		if err != nil || got != k {
			t.Errorf("ParseProcessKey(%q) = %+v, %v; want %+v", s, got, err, k)
		}
		b, err := json.Marshal(struct{ K ProcessKey }{k})
		if err != nil {
			t.Fatal(err)
		}
		var back struct{ K ProcessKey }
		if err := json.Unmarshal(b, &back); err != nil || back.K != k {
			t.Errorf("json round trip of %q: %+v, %v", name, back.K, err)
		}
	}
}

func TestParseProcessKeyErrors(t *testing.T) {
	for _, s := range []string{"", "abc", "zz.bmFtZQ", "0123456789abcdef.", "0123456789abcdef.!!!", "123.bmFtZQ"} {
		if _, err := ParseProcessKey(s); !errors.Is(err, ErrBadProcessKey) {
			t.Errorf("ParseProcessKey(%q) error = %v, want ErrBadProcessKey", s, err)
		}
	}
}

func TestProcessKeyHashIsStable(t *testing.T) {
	// cityHash64('') in ClickHouse is 0x9ae16a3b2f90404f.
	if got := NewProcessKey("x", "").CmdHash; got != 0x9ae16a3b2f90404f {
		t.Errorf("hash of empty cmd = %#x", got)
	}
}
