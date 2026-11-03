package guard

import "testing"

// T-0874: a researcher's gh api must resolve to GET in every spelling (same cases as tests/test_worker_guard_researcher.py).
func TestResearcherGhAPIMethod(t *testing.T) {
	clearTermsEnv(t)
	denied := []string{"gh api -XPOST repos/a/b/issues", "gh api --method=POST x", "gh api -X=post x", "gh api x -f title=t",
		"gh api x -F a=1", "gh api x --input f.json", "gh api -X PATCH x", "gh api -X DELETE x",
		"gh api x --field a=1", "gh api x --raw-field a=b", "gh api -iXPOST x", "gh api -if a=1 x",
		"gh api --method=get x -f a=1", "gh api -X GET -X POST x", "gh api --method put x"}
	allowed := []string{"gh api repos/a/b", "gh api -X GET repos/a/b", "gh api --method=GET repos/a/b/pulls?state=open",
		"gh api -XGET x", "gh api -X=get x", "gh api --method get x", "gh api x --jq .name"}
	for _, c := range denied {
		if err := Check(c, "", "researcher", false); err == nil {
			t.Errorf("should block: %q", c)
		}
	}
	for _, c := range allowed {
		if err := Check(c, "", "researcher", false); err != nil {
			t.Errorf("should allow: %q: %v", c, err)
		}
	}
}
