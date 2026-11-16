package macbox

import "testing"

func TestAccountForRole(t *testing.T) {
	cases := []struct {
		role, want string
		ok         bool
	}{
		{"reviewer", AccountRO, true},
		{"qa", AccountRO, true},
		{"researcher", AccountRO, true},
		{"coder", AccountRW, true},
		{"boss", "", false},
	}
	for _, c := range cases {
		got, err := AccountForRole(c.role)
		if c.ok {
			if err != nil || got != c.want {
				t.Fatalf("role %s: got %q err %v want %q", c.role, got, err, c.want)
			}
		} else if err == nil {
			t.Fatalf("role %s: expected error", c.role)
		}
	}
}