package checklist

import (
	"strings"
	"testing"
)

func item(id, extra string) string {
	return `{"id":"` + id + `","title":"T ` + id + `","check":"C ` + id + `","sources":["PR#1","PR#2"],"trigger":` + extra + `}`
}

func file(mode string, items ...string) []byte {
	return []byte(`{"version":1,"mode":"` + mode + `","items":[` + strings.Join(items, ",") + `]}`)
}

func mustLoad(t *testing.T, data []byte) *List {
	t.Helper()
	l, err := Load(data)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return l
}

func TestMatch(t *testing.T) {
	cases := []struct {
		name    string
		trigger string
		changes []Change
		want    bool
	}{
		{"paths only", `{"paths":["roles/hooks/*"]}`, []Change{{Path: "roles/hooks/worker-guard"}}, true},
		{"paths no hit", `{"paths":["roles/hooks/*"]}`, []Change{{Path: "docs/a.md"}}, false},
		{"paths and added match", `{"paths":["go/**"],"added":["os\\.Exit"]}`, []Change{{Path: "go/cmd/x/main.go", Added: []string{"\tos.Exit(2)"}}}, true},
		{"paths match added does not", `{"paths":["go/**"],"added":["os\\.Exit"]}`, []Change{{Path: "go/cmd/x/main.go", Added: []string{"return 0"}}}, false},
		{"added in other file does not count", `{"paths":["go/**"],"added":["TODO"]}`, []Change{{Path: "go/a.go"}, {Path: "docs/b.md", Added: []string{"TODO"}}}, false},
		{"added only applies to all files", `{"added":["TODO"]}`, []Change{{Path: "docs/b.md", Added: []string{"a TODO"}}}, true},
		{"exclude_paths", `{"paths":["go/**"],"exclude_paths":["**/*_test.go"]}`, []Change{{Path: "go/x/a_test.go"}}, false},
		{"exclude_paths other file still matches", `{"paths":["go/**"],"exclude_paths":["**/*_test.go"]}`, []Change{{Path: "go/x/a_test.go"}, {Path: "go/x/a.go"}}, true},
		{"star does not cross dir", `{"paths":["ci/*.py"]}`, []Change{{Path: "ci/sub/a.py"}}, false},
		{"star one level", `{"paths":["ci/*.py"]}`, []Change{{Path: "ci/a.py"}}, true},
		{"double star crosses dir", `{"paths":["ci/**/*.py"]}`, []Change{{Path: "ci/sub/deep/a.py"}}, true},
		{"double star slash matches zero dirs", `{"paths":["ci/**/*.py"]}`, []Change{{Path: "ci/a.py"}}, true},
		{"deleted file counts", `{"paths":["roles/**"]}`, []Change{{Path: "roles/old.md"}}, true},
		{"deleted file has no added lines", `{"paths":["roles/**"],"added":["x"]}`, []Change{{Path: "roles/old.md"}}, false},
		{"non-ascii path", `{"paths":["docs/*.md"]}`, []Change{{Path: "docs/пример-файл.md"}}, true},
		{"non-ascii literal glob", `{"paths":["docs/файл?.md"]}`, []Change{{Path: "docs/файл1.md"}}, true},
		{"non-ascii added line", `{"paths":["docs/*"],"added":["(?i)ошибка"]}`, []Change{{Path: "docs/a.md", Added: []string{"Это ОШИБКА здесь"}}}, true},
		{"non-ascii added no match", `{"paths":["docs/*"],"added":["ошибка"]}`, []Change{{Path: "docs/a.md", Added: []string{"всё хорошо"}}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := mustLoad(t, file("observe", item("CL-01", tc.trigger)))
			got := len(Match(l.Items, tc.changes)) == 1
			if got != tc.want {
				t.Fatalf("matched=%v want %v", got, tc.want)
			}
		})
	}
}

func TestMatchKeepsFileOrder(t *testing.T) {
	l := mustLoad(t, file("observe", item("CL-02", `{"paths":["a/*"]}`), item("CL-01", `{"paths":["a/*"]}`)))
	m := Match(l.Items, []Change{{Path: "a/x"}})
	if len(m) != 2 || m[0].ID != "CL-02" || m[1].ID != "CL-01" {
		t.Fatalf("order: %+v", m)
	}
}

func TestLoadValid(t *testing.T) {
	l := mustLoad(t, file("enforce", item("CL-01", `{"paths":["a"]}`), item("CL-100", `{"added":["x"]}`)))
	if l.Mode != "enforce" || len(l.Items) != 2 || l.Version != 1 {
		t.Fatalf("got %+v", l)
	}
}

func TestLoadInvalid(t *testing.T) {
	ok := item("CL-01", `{"paths":["a"]}`)
	cases := []struct {
		name string
		data string
		want []string
	}{
		{"duplicate id", string(file("observe", ok, ok)), []string{"item CL-01", "field id", "duplicate"}},
		{"unknown item key", `{"version":1,"mode":"observe","items":[{"id":"CL-01","title":"t","check":"c","sources":["PR#1","PR#2"],"trigger":{"paths":["a"]},"extra":1}]}`, []string{"unknown key", "extra"}},
		{"unknown top key", `{"version":1,"mode":"observe","items":[],"zzz":1}`, []string{"unknown key", "zzz"}},
		{"unknown trigger key", string(file("observe", item("CL-01", `{"paths":["a"],"regex":["x"]}`))), []string{"item CL-01", "field trigger", "regex"}},
		{"bad regex", string(file("observe", item("CL-01", `{"added":["("]}`))), []string{"item CL-01", "trigger.added[0]", "bad regex"}},
		{"bad glob", string(file("observe", item("CL-01", `{"paths":["a/[b"]}`))), []string{"item CL-01", "trigger.paths[0]", "bad glob"}},
		{"empty trigger", string(file("observe", item("CL-01", `{}`))), []string{"item CL-01", "empty trigger"}},
		{"empty paths array", string(file("observe", item("CL-01", `{"paths":[]}`))), []string{"item CL-01", "trigger.paths"}},
		{"one source", `{"version":1,"mode":"observe","items":[{"id":"CL-01","title":"t","check":"c","sources":["PR#1"],"trigger":{"paths":["a"]}}]}`, []string{"item CL-01", "field sources", "at least 2"}},
		{"same source twice", `{"version":1,"mode":"observe","items":[{"id":"CL-01","title":"t","check":"c","sources":["PR#1","PR#1"],"trigger":{"paths":["a"]}}]}`, []string{"field sources", "at least 2"}},
		{"bad source form", `{"version":1,"mode":"observe","items":[{"id":"CL-01","title":"t","check":"c","sources":["PR#1","#2"],"trigger":{"paths":["a"]}}]}`, []string{"field sources[1]", "PR#<digits>"}},
		{"bad id", string(file("observe", item("CL-1", `{"paths":["a"]}`))), []string{"field id", "CL-"}},
		{"empty title", `{"version":1,"mode":"observe","items":[{"id":"CL-01","title":"","check":"c","sources":["PR#1","PR#2"],"trigger":{"paths":["a"]}}]}`, []string{"item CL-01", "field title"}},
		{"missing check", `{"version":1,"mode":"observe","items":[{"id":"CL-01","title":"t","sources":["PR#1","PR#2"],"trigger":{"paths":["a"]}}]}`, []string{"item CL-01", "field check", "required"}},
		{"bad version", `{"version":2,"mode":"observe","items":[]}`, []string{"field version"}},
		{"bad mode", `{"version":1,"mode":"warn","items":[]}`, []string{"field mode"}},
		{"not json", `nope`, []string{"JSON object"}},
		{"trailing data", `{"version":1,"mode":"observe","items":[]} {}`, []string{"JSON object"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load([]byte(tc.data))
			if err == nil {
				t.Fatal("expected a validation error")
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q lacks %q", err.Error(), w)
				}
			}
		})
	}
}

func TestParseDiff(t *testing.T) {
	diff := `diff --git a/go/a.go b/go/a.go
index 1..2 100644
--- a/go/a.go
+++ b/go/a.go
@@ -1,2 +1,3 @@
 ctx
-old
+new one
++weird
diff --git a/old.md b/old.md
deleted file mode 100644
index 1..0
--- a/old.md
+++ /dev/null
@@ -1 +0,0 @@
-gone
diff --git a/x/a.txt b/x/b.txt
similarity index 100%
rename from x/a.txt
rename to x/b.txt
diff --git a/img.png b/img.png
deleted file mode 100644
Binary files a/img.png and /dev/null differ
diff --git a/docs/файл with space.md b/docs/файл with space.md
new file mode 100644
--- /dev/null
+++ b/docs/файл with space.md
@@ -0,0 +1 @@
+строка
diff --git "a/q\"t\tab.bin" "b/q\"t\tab.bin"
old mode 100644
new mode 100755
`
	got := ParseDiff(diff)
	want := []struct {
		path  string
		added []string
	}{
		{"go/a.go", []string{"new one", "+weird"}},
		{"old.md", nil},
		{"x/b.txt", nil},
		{"img.png", nil},
		{"docs/файл with space.md", []string{"строка"}},
		{"q\"t\tab.bin", nil},
		{"x/a.txt", nil},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d changes: %+v", len(got), got)
	}
	for i, w := range want {
		if got[i].Path != w.path || strings.Join(got[i].Added, "|") != strings.Join(w.added, "|") {
			t.Errorf("change %d = %+v want %+v", i, got[i], w)
		}
	}
}

func TestParseDiffQuotedPath(t *testing.T) {
	diff := "diff --git \"a/t\\303\\251st.md\" \"b/t\\303\\251st.md\"\n--- \"a/t\\303\\251st.md\"\n+++ \"b/t\\303\\251st.md\"\n@@ -0,0 +1 @@\n+x\n"
	got := ParseDiff(diff)
	if len(got) != 1 || got[0].Path != "tést.md" {
		t.Fatalf("got %+v", got)
	}
}

func TestRenderBlock(t *testing.T) {
	l := mustLoad(t, file("observe", item("CL-01", `{"paths":["a"]}`)))
	got := RenderBlock("origin/main", "abc1234", "observe", l.Items, 3)
	want := "<details><summary>Diff checklist</summary>\n\n## Diff checklist\nitems: origin/main@abc1234 · mode: observe · matched 1 of 3\n- CL-01 T CL-01 | check: C CL-01 | result: TODO\n</details>\n"
	if got != want {
		t.Fatalf("got\n%q\nwant\n%q", got, want)
	}
	none := RenderBlock("r", "s", "enforce", nil, 2)
	if !strings.Contains(none, "- none matched\n") || !strings.Contains(none, "matched 0 of 2") {
		t.Fatalf("none block: %q", none)
	}
}

func TestCheckBody(t *testing.T) {
	l := mustLoad(t, file("enforce",
		item("CL-01", `{"paths":["a"]}`), item("CL-02", `{"paths":["a"]}`), item("CL-03", `{"paths":["a"]}`),
		item("CL-04", `{"paths":["a"]}`), item("CL-05", `{"paths":["a"]}`), item("CL-06", `{"paths":["a"]}`)))
	body := "intro\n<details><summary>Diff checklist</summary>\n\n## Diff checklist\nitems: x\n" +
		"- CL-01 T | check: C | result: PASS ran the test\n" +
		"- CL-02 T | check: a | b | result: TODO\n" +
		"- CL-03 T | check: C | result: FAIL still broken\n" +
		"- CL-04 T | check: C | result: N/A\n" +
		"- CL-05 T | check: C | result: N/A docs only\n" +
		"</details>\n"
	got := CheckBody(l.Items, body)
	want := []string{"OK CL-01", "TODO CL-02: replace TODO", "FAIL CL-03: still broken", "TODO CL-04: N/A needs a reason", "OK CL-05", "MISSING CL-06: no"}
	for i, w := range want {
		if !strings.HasPrefix(got[i].Line(), w) {
			t.Errorf("line %d = %q want prefix %q", i, got[i].Line(), w)
		}
	}
	// A result line outside the block does not count.
	out := CheckBody(l.Items[:1], "- CL-01 T | check: C | result: PASS x\n")
	if out[0].Status != "MISSING" {
		t.Errorf("outside block: %+v", out[0])
	}
}

func FuzzLoad(f *testing.F) {
	f.Add(file("observe", item("CL-01", `{"paths":["a/**"],"added":["x"]}`)))
	f.Add([]byte(`{}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if l, err := Load(data); err == nil {
			Match(l.Items, []Change{{Path: "a/b", Added: []string{"x"}}})
		}
	})
}

func FuzzParseDiff(f *testing.F) {
	f.Add("diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n+y\n")
	f.Fuzz(func(t *testing.T, s string) { ParseDiff(s) })
}
