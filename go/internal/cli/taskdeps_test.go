package cli

import (
	"reflect"
	"testing"
)

func TestSplitAfter(t *testing.T) { // split_after: trimmed, empties and duplicates dropped, order kept
	for _, tc := range []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"T-1", []string{"T-1"}},
		{" T-2 , T-1,T-2 ,,", []string{"T-2", "T-1"}},
		{"T-1 ,　T-2", []string{"T-1", "T-2"}},
	} {
		if got := splitAfter(tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("splitAfter(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
