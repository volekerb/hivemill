package macfacts

import "regexp"

// Redacted replaces anything that looks like a secret.
const Redacted = "<redacted>"

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{20,}`),
	regexp.MustCompile(`github_pat_[A-Za-z0-9_]{20,}`),
	regexp.MustCompile(`sk-ant-[A-Za-z0-9_\-]+`),
	regexp.MustCompile(`[0-9a-fA-F]{40,}`),
}

// Redact replaces token-shaped strings (GitHub tokens, Anthropic keys, 40+ hex
// characters) with "<redacted>".
func Redact(s string) string {
	for _, re := range secretPatterns {
		s = re.ReplaceAllString(s, Redacted)
	}
	return s
}
