package cli

import (
	"reflect"
	"testing"
)

func TestResPSArgs(t *testing.T) {
	a, hdr := resPSArgs("linux")
	if !reflect.DeepEqual(a, []string{"-eo", "rss,args", "--sort=-rss"}) || !hdr {
		t.Fatalf("linux ps = %v %v; must stay the GNU form", a, hdr)
	}
	a, hdr = resPSArgs("darwin")
	if !reflect.DeepEqual(a, []string{"-axo", "rss=,command=", "-m"}) || hdr {
		t.Fatalf("darwin ps = %v %v", a, hdr)
	}
}
