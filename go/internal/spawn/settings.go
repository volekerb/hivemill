package spawn

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// Refusal is a Python die(msg, code): the text goes to stderr as "factory: <Msg>" and the process exits with Code.
type Refusal struct {
	Msg    string
	Code   int
	Reason string // the tick key of a refused queued row ("goal-token-limit", "settings-render"), "" otherwise
}

func (r *Refusal) Error() string { return fmt.Sprintf("factory: %s (exit %d)", r.Msg, r.Code) }

func refuse(code int, format string, a ...any) *Refusal {
	return &Refusal{Msg: fmt.Sprintf(format, a...), Code: code}
}

// Token is one @FACTORY_*@ substitution of settings_tokens().
type Token struct{ Name, Value string }

// SettingsTokens builds the token list in settings_tokens() order from the three resolved values.
func SettingsTokens(liveHome, homeDir, userConfig string) []Token {
	return []Token{{"@FACTORY_LIVE_HOME@", liveHome}, {"@FACTORY_HOME_DIR@", homeDir}, {"@FACTORY_USER_CONFIG@", userConfig}}
}

// CheckLivePin is the live-home pin of settings_tokens(): a live FACTORY_HOME renders only when the live home, the
// running checkout and FACTORY_HOME are one directory (all three already resolved by the caller).
func CheckLivePin(homeIsLive bool, liveHome, runRoot, home string) *Refusal {
	if homeIsLive && !(liveHome == runRoot && runRoot == home) {
		return refuse(3, "live_home %s != running checkout %s: refusing to render settings "+
			"(FACTORY_HOME=%s); fix or remove ~/.config/factory/live_home, or run the live checkout's bin/factory", liveHome, runRoot, home)
	}
	return nil
}

// SettingsText is settings_text(): the template with every token substituted (JSON-escaped, so a quote or backslash
// in a path cannot add rules). templatePath only names the file in messages. A result that is not valid JSON, or that
// still holds a @FACTORY_ token, is a refusal (exit 3): a typo must stop the spawn, not start an agent without its
// deny rules.
func SettingsText(template, templatePath string, tokens []Token) (string, *Refusal) {
	text := template
	for _, t := range tokens {
		text = strings.ReplaceAll(text, t.Name, jsonEscape(t.Value))
	}
	if !json.Valid([]byte(text)) {
		var v any
		err := json.Unmarshal([]byte(text), &v)
		return "", refuse(3, "settings template %s renders to invalid JSON (%v); fix the template", templatePath, err)
	}
	if strings.Contains(text, "@FACTORY_") {
		names := make([]string, len(tokens))
		for i, t := range tokens {
			names[i] = t.Name
		}
		return "", refuse(3, "settings template %s has an unknown @FACTORY_ token; known: %s", templatePath, strings.Join(names, ", "))
	}
	return text, nil
}

// SettingsFromDir reads rolesDir/settings/<role>.json.in and renders it.
func SettingsFromDir(rolesDir, role string, tokens []Token) (string, *Refusal) {
	path := filepath.Join(rolesDir, "settings", role+".json.in")
	tpl, err := ReadText(path)
	if err != nil {
		return "", refuse(3, "cannot read the settings template %s: %v; restore it with git checkout", path, err)
	}
	return SettingsText(tpl, path, tokens)
}
