package scout

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"
)

type riskCase struct {
	Name      string            `json:"name"`
	Prefix    string            `json:"prefix"`
	Truncated bool              `json:"truncated"`
	Tree      []Entry           `json:"tree"`
	Blobs     map[string]string `json:"blobs"`
	Mode      string            `json:"mode"`
	Want      struct {
		Risk   string   `json:"risk"`
		Detail []string `json:"detail"`
		Kind   string   `json:"kind"`
	} `json:"want"`
}

// TestRiskGolden: Classify equals scout/risk.py classify() on every fixture (goldens generated from the Python).
func TestRiskGolden(t *testing.T) {
	raw, err := os.ReadFile("testdata/risk_cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []riskCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 60 {
		t.Fatalf("only %d fixtures", len(cases))
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			var read ReadFunc
			switch c.Mode {
			case "nil":
			case "unreadable":
				read = func(Entry) (string, error) { return "", &ReadError{"boom"} }
			default:
				read = func(e Entry) (string, error) {
					if b, ok := c.Blobs[e.Sha]; ok {
						return b, nil
					}
					return "", &ReadError{"missing"}
				}
			}
			got := Classify(c.Tree, c.Prefix, read, c.Truncated)
			if got.Risk != c.Want.Risk || got.Kind != c.Want.Kind || !reflect.DeepEqual(append([]string{}, got.Detail...), append([]string{}, c.Want.Detail...)) {
				t.Errorf("got  %q %q %q\nwant %q %q %q", got.Risk, got.Kind, got.Detail, c.Want.Risk, c.Want.Kind, c.Want.Detail)
			}
		})
	}
}

func TestReadErrorIsAnError(t *testing.T) {
	var err error = &ReadError{"x"}
	var re *ReadError
	if !errors.As(err, &re) || re.Error() != "x" {
		t.Fatal("ReadError does not behave as an error")
	}
}

func TestHasManifestAndAllowed(t *testing.T) {
	tree := []Entry{{Path: "p/SKILL.md", Type: "blob"}, {Path: "q/agents/a.md", Type: "blob"}, {Path: "r/x.txt", Type: "blob"}}
	if !HasManifest(tree, "p") || !HasManifest(tree, "q") || HasManifest(tree, "r") {
		t.Error("HasManifest wrong")
	}
	for p, want := range map[string]bool{"a.md": true, "x/skills/s/d.json": true, "d.json": false, "LICENSE-APACHE": true, "run.py": false, ".claude-plugin/plugin.json": true} {
		if IsAllowed(p) != want {
			t.Errorf("IsAllowed(%q) != %v", p, want)
		}
	}
}
