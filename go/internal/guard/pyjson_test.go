package guard

import (
	"encoding/json"
	"os"
	"testing"
)

// T-0352 B2: PyJSONError must give the text of Python 3.13's json.loads error for the same input. The table in
// testdata/pyjson_errors.json is [text, "JSONDecodeError: ..." | null] pairs recorded from json.loads; null = valid.
func TestPyJSONErrorMatchesPython(t *testing.T) {
	raw, err := os.ReadFile("testdata/pyjson_errors.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases [][2]*string
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 40 {
		t.Fatalf("only %d recorded cases", len(cases))
	}
	for _, c := range cases {
		want := ""
		if c[1] != nil {
			want = *c[1]
		}
		if got := PyJSONError(*c[0]); got != want {
			t.Errorf("PyJSONError(%q) = %q, want %q", *c[0], got, want)
		}
	}
}
