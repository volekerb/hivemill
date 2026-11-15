package scout

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"strings"
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
		for _, h := range ScreenText(c.Name, resolveBlobs(c.Text, uint64(i)), own) {
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

const blobAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"

var blobPlaceholder = regexp.MustCompile(`<<B(\d+)>>`)

// resolveBlobs replaces each <<Bn>> in order with n base64-alphabet characters from a 64-bit LCG seeded by the case
// index. The corpus file stores only the placeholders (so no base64-like text is tracked); the Python oracle that
// produced the expected hits used the same generator on the same text, so the parity run covers the same inputs.
func resolveBlobs(text string, seed uint64) string {
	x := seed
	return blobPlaceholder.ReplaceAllStringFunc(text, func(m string) string {
		n, _ := strconv.Atoi(blobPlaceholder.FindStringSubmatch(m)[1])
		var sb strings.Builder
		for i := 0; i < n; i++ {
			x = x*6364136223846793005 + 1442695040888963407
			sb.WriteByte(blobAlphabet[(x>>33)%64])
		}
		return sb.String()
	})
}
