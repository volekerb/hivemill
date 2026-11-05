package guard

import "testing"

func TestConfigParseAndIsRead(t *testing.T) {
	for _, c := range []struct {
		args []string
		read bool
		ok   bool
	}{
		{[]string{"--get", "user.name"}, true, true},
		{[]string{"--list"}, true, true},
		{[]string{"user.name"}, true, true},
		{[]string{"user.name", "x"}, false, true},
		{[]string{"--unset", "k"}, false, false},
		{[]string{"set", "k", "v"}, false, true},
		{[]string{"get", "k"}, true, true},
		{[]string{"list"}, true, true},
		{[]string{"--type", "bool", "k"}, true, true},
		{[]string{"--default", "d", "--get", "k"}, true, true},
		{[]string{"--comment", "--get", "alias.x", "push"}, false, true},
		{[]string{"--get", "alias.x", "--show-origin"}, true, true},
		{[]string{"alias.x", "push", "--get"}, false, false},
		{[]string{"alias.y", "--null"}, false, true},
		{[]string{"--get", "alias.x", "--unset"}, false, false},
		{[]string{"--ge", "user.name"}, false, false},
		{[]string{"--get", "--", "alias.x"}, false, false},
	} {
		flags, pos, ok := configParse(c.args)
		if ok != c.ok {
			t.Errorf("configParse(%q) ok=%v want %v flags=%v pos=%v", c.args, ok, c.ok, flags, pos)
			continue
		}
		if !ok {
			continue
		}
		if got := configIsRead(c.args); got != c.read {
			t.Errorf("configIsRead(%q) = %v, want %v (flags=%v pos=%v)", c.args, got, c.read, flags, pos)
		}
	}
}

func TestGitConfigMutates(t *testing.T) {
	for _, c := range []struct {
		args       []string
		mut, write bool
	}{
		{[]string{"--get", "user.name"}, false, false},
		{[]string{"--list"}, false, false},
		{[]string{"user.name"}, false, false},
		{[]string{"user.name", "x"}, true, true},
		{[]string{"--unset", "k"}, true, true},
		{[]string{"--add", "k", "v"}, true, true},
		{[]string{"set", "k", "v"}, true, true},
		{[]string{"unset", "k"}, true, true},
		{[]string{"edit"}, true, true},
		{[]string{"get", "k"}, false, false},
		{[]string{"list"}, false, false},
		{[]string{"--global", "k", "v"}, true, true},
		{[]string{"--type", "bool", "k"}, false, false},
		{[]string{"--default", "d", "--get", "k"}, false, false},
		{[]string{"--show-origin", "k", "v"}, true, true},
		{[]string{"--edit"}, true, true},
		{[]string{"-e"}, true, true},
	} {
		if got := configMutates(c.args); got != c.mut {
			t.Errorf("configMutates(%q) = %v, want %v", c.args, got, c.mut)
		}
		if got := configArgsWrite(c.args); got != c.write {
			t.Errorf("configArgsWrite(%q) = %v, want %v", c.args, got, c.write)
		}
	}
}
