package buildinfo

import "testing"

func TestString(t *testing.T) {
	tests := []struct {
		name string
		got  string
		want string
	}{
		{"version line", String(), "factory-go " + Version},
		{"version constant", Version, "0.1.0"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Fatalf("got %q, want %q", tc.got, tc.want)
			}
		})
	}
}
