package hm

import (
	"os"
	"regexp"
	"slices"
	"strings"
)

// The App Store Connect API key must not reach runner logs, PR comments, PR bodies or recorded prompts (G-075).

const ascRedacted = "[asc-key redacted]"

// minSecretLen keeps a short or empty value from redacting ordinary text.
const minSecretLen = 8

// pemKeyRe also matches a block with escaped \n newlines, as in a JSON key file.
var pemKeyRe = regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`)

var (
	ascValueEnvs = []string{"APP_STORE_CONNECT_API_KEY_KEY", "APP_STORE_CONNECT_API_KEY_KEY_ID",
		"APP_STORE_CONNECT_API_KEY_ISSUER_ID", "ASC_KEY_CONTENT", "ASC_KEY_ID", "ASC_ISSUER_ID"}
	ascPathEnvs = []string{"APP_STORE_CONNECT_API_KEY_PATH", "ASC_KEY_PATH"}
)

// ScrubSecrets replaces PEM private key blocks and the App Store Connect key the environment names (values and key
// files) with ascRedacted. Text with no secret is returned unchanged.
func ScrubSecrets(text string) string {
	// blocks first: replacing single key file lines would break a block apart
	text = pemKeyRe.ReplaceAllLiteralString(text, ascRedacted)
	var secrets []string
	add := func(s string) {
		if len(s) >= minSecretLen {
			secrets = append(secrets, s)
		}
	}
	for _, name := range ascValueEnvs {
		add(os.Getenv(name))
	}
	for _, name := range ascPathEnvs {
		path := os.Getenv(name)
		if path == "" {
			continue
		}
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		add(string(b))
		for _, line := range strings.Split(string(b), "\n") {
			add(strings.TrimSuffix(line, "\r"))
		}
	}
	// longest first: a value inside a longer one must not leave the rest of the longer one behind
	slices.SortStableFunc(secrets, func(a, b string) int { return len(b) - len(a) })
	for _, s := range secrets {
		text = strings.ReplaceAll(text, s, ascRedacted)
	}
	return text
}
