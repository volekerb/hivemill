package views

import (
	"regexp"
	"sort"
)

// Routes is web/views/__init__.py ROUTES: part 1 (T-1475) is home, goals and tasks, part 2 (T-1476) the rest.
var Routes = map[string]View{
	"/":           homeView{},
	"/goals":      goalsView{},
	"/goal/<id>":  goalDetailView{},
	"/tasks":      tasksView{},
	"/task/<id>":  taskDetailView{},
	"/agents":     agentsView{},
	"/resources":  resourcesView{},
	"/throughput": throughputView{},
	"/quality":    qualityView{},
	"/changelog":  changelogView{},
	"/tokens":     tokensView{},
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
