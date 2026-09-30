package vault

import "regexp"

// SecretPatterns is vault rule 4: no seeds, keys, or passwords, because this repo is
// pushed to GitHub. A false positive here is cheap; a leaked seed is not.
//
// This lives in the shared package because two callers need the same list: the validator
// checks committed notes, and the activity extractor checks text on its way out of a
// private folder. Those must never drift apart.
var SecretPatterns = []struct {
	Name    string
	Pattern *regexp.Regexp
}{
	{"private key block", regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`)},
	{"AWS access key", regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`)},
	{"GitHub token", regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{36,}`)},
	{"Slack token", regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{10,}`)},
	{"Anthropic key", regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{20,}`)},
	{"OpenAI key", regexp.MustCompile(`\bsk-[A-Za-z0-9]{32,}\b`)},
	{"hex private key", regexp.MustCompile(`(?i)\b(priv(ate)?[_-]?key|secret[_-]?key)\b\s*[:=]\s*["']?(0x)?[0-9a-f]{64}\b`)},
	{"assigned password", regexp.MustCompile(`(?i)\b(password|passphrase|api[_-]?key|secret)\b\s*[:=]\s*["'][^"']{8,}["']`)},
	{"mnemonic seed", regexp.MustCompile(`(?i)\b(seed|mnemonic)\s*(phrase)?\s*[:=]\s*(\w+\s+){11,}\w+`)},
}

// MatchSecret returns the name of the first secret pattern the text matches, or "".
func MatchSecret(text string) string {
	for _, candidate := range SecretPatterns {
		if candidate.Pattern.MatchString(text) {
			return candidate.Name
		}
	}
	return ""
}
