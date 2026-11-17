package check

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// sensitiveKeys are lowercase fragments; a JSON key containing one must hold an empty value or a <placeholder>.
var sensitiveKeys = []string{"token", "password", "secret", "apikey", "api_key", "api-key", "privatekey", "private_key", "private-key"}

var (
	placeholderRe = regexp.MustCompile(`^<[^<>]+>$`)
	// textSecretRe finds the unmistakable credential shapes in a non-JSON fixture file.
	textSecretRe = regexp.MustCompile(`(^|[^A-Za-z0-9_])(ghp_|github_pat_)[A-Za-z0-9_]{6,}|-----BEGIN`)
)

// scanSecrets looks for credentials in the example config, the eval cases and the eval fixtures of one plugin.
func scanSecrets(root, plugin, name string, c *collector) {
	base := filepath.Join(root, filepath.FromSlash(plugin))
	files := []string{filepath.Join(base, name+".example.json")}
	for _, sub := range []string{"cases", "fixtures"} {
		_ = filepath.WalkDir(filepath.Join(base, "eval", sub), func(p string, d fs.DirEntry, err error) error {
			if err == nil && d.Type().IsRegular() {
				files = append(files, p)
			}
			return nil
		})
	}
	sort.Strings(files)
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue // a missing example config is reported by the plugin checks
		}
		rel, _ := filepath.Rel(root, f)
		var doc any
		if strings.HasSuffix(f, ".json") && json.Unmarshal(b, &doc) == nil {
			walk(doc, "", rel, c)
		} else if textSecretRe.Match(b) {
			c.add(rel, "contains a credential-looking value (ghp_, github_pat_ or a PEM block)")
		}
	}
}

// walk visits every value of a decoded JSON document; key is the nearest enclosing object key.
func walk(v any, key, rel string, c *collector) {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			walk(t[k], k, rel, c)
		}
	case []any:
		for _, e := range t {
			walk(e, key, rel, c)
		}
	case string:
		if reason := secretReason(key, t); reason != "" {
			c.add(rel, reason)
		}
	}
}

func secretReason(key, val string) string {
	switch {
	case strings.HasPrefix(val, "ghp_"), strings.HasPrefix(val, "github_pat_"), strings.HasPrefix(val, "sk-"),
		strings.Contains(val, "-----BEGIN"):
		return "value under key " + quote(key) + " looks like a credential (ghp_, github_pat_, sk- or a PEM block)"
	}
	lk := strings.ToLower(key)
	for _, frag := range sensitiveKeys {
		if strings.Contains(lk, frag) && val != "" && !placeholderRe.MatchString(val) {
			return "key " + quote(key) + " holds a non-placeholder value; use an empty string or <angle-bracket placeholder>"
		}
	}
	return ""
}

func quote(s string) string { return `"` + s + `"` }
