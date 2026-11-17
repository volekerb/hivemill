package views

import (
	"regexp"
	"sort"
)

// Routes is the part-1 slice of web/views/__init__.py ROUTES: the five keys of home, goals and tasks. The remaining
// keys (agents, resources, throughput, quality, changelog, tokens) come with the part-2 views (T-1476).
var Routes = map[string]View{
	"/":          homeView{},
	"/goals":     goalsView{},
	"/goal/<id>": goalDetailView{},
	"/tasks":     tasksView{},
	"/task/<id>": taskDetailView{},
}

// RouteKeys is the sorted list of route keys.
func RouteKeys() []string {
	keys := make([]string, 0, len(Routes))
	for k := range Routes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// idRe is web/server.py _ID_RE: ^/(goal|task)/([A-Z]-\d+)$ with Python's Unicode \d and a $ that also matches
// before one trailing newline.
var idRe = regexp.MustCompile(`^/(goal|task)/([A-Z]-\p{Nd}+)\n?\z`)

// Match resolves a request path like web/server.py _route: an exact route key first, then "/<kind>/<id>". active is
// the nav entry to mark ("/goal/G-1" marks "/goals"). ok is false for a path with no view (a 404).
func Match(path string) (v View, params map[string]string, active string, ok bool) {
	if v, ok := Routes[path]; ok {
		return v, map[string]string{}, path, true
	}
	if m := idRe.FindStringSubmatch(path); m != nil {
		if v, ok := Routes["/"+m[1]+"/<id>"]; ok {
			return v, map[string]string{"id": m[2]}, "/" + m[1] + "s", true
		}
	}
	return nil, nil, "", false
}
