package scout

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"testing"
)

// TestPrescreenGolden: the hits of every corpus file equal scout/vendor.py prescreen() (the corpus mixes hand-written
// adversarial lines with seeded random combinations of them).
func TestPrescreenGolden(t *testing.T) {
	raw, err := os.ReadFile("testdata/prescreen_golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name string
		Text string
		Own  *string
		Want [][2]any
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 300 {
		t.Fatalf("only %d cases", len(cases))
	}
	bad := 0
	for i, c := range cases {
		own := ""
		if c.Own != nil {
			own = *c.Own
		}
		var got [][2]any
		for _, h := range ScreenText(c.Name, c.Text, own) {
			got = append(got, [2]any{float64(h.Line), h.Pattern})
		}
		if !reflect.DeepEqual(got, c.Want) && !(len(got) == 0 && len(c.Want) == 0) {
			bad++
			if bad <= 8 {
				t.Errorf("case %d (%s, own=%q):\ntext %q\n got %v\nwant %v", i, c.Name, own, truncate(c.Text), got, c.Want)
			}
		}
	}
	if bad > 0 {
		t.Errorf("%d of %d corpus files differ from Python", bad, len(cases))
	}
}

func truncate(s string) string {
	if len(s) > 400 {
		return fmt.Sprintf("%s...(%d bytes)", s[:400], len(s))
	}
	return s
}
