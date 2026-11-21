package alerter

import (
	"encoding/json"
	"os"
	"strconv"
	"testing"
)

// The cases are shared with the Mac tests (ProcCulpritTests).
func TestCulpritLabel(t *testing.T) {
	raw, err := os.ReadFile("testdata/culprit_cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name   string `json:"name"`
		Cmd    string `json:"cmd"`
		Cwd    string `json:"cwd"`
		Repo   string `json:"repo"`
		PID    int32  `json:"pid"`
		What   string `json:"what"`
		Folder string `json:"folder"`
		Label  string `json:"label"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 30 {
		t.Fatalf("cases = %d", len(cases))
	}
	for i, tc := range cases {
		t.Run(strconv.Itoa(i)+"_"+tc.Name, func(t *testing.T) {
			c := culprit{pid: tc.PID, name: tc.Name, cmd: tc.Cmd, cwd: tc.Cwd, repo: tc.Repo}
			if got := c.what(); got != tc.What {
				t.Errorf("what = %q, want %q", got, tc.What)
			}
			if got := c.folder(); got != tc.Folder {
				t.Errorf("folder = %q, want %q", got, tc.Folder)
			}
			if got := c.label(); got != tc.Label {
				t.Errorf("label = %q, want %q", got, tc.Label)
			}
		})
	}
}
