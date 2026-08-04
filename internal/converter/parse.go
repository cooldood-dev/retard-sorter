package converter

import "strings"

// Credential is a single email:password line.
type Credential struct {
	Email    string
	Password string
	Raw      string
}

// ParseCredential splits one line on the first colon. Blank lines and lines
// without a usable delimiter are reported as invalid via the ok flag.
// Whitespace around both parts is trimmed.
func ParseCredential(line string) (Credential, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return Credential{}, false
	}
	parts := strings.SplitN(trimmed, ":", 2)
	email := strings.TrimSpace(parts[0])
	if len(parts) != 2 || email == "" {
		return Credential{}, false
	}
	pass := strings.TrimSpace(parts[1])
	if pass == "" {
		return Credential{}, false
	}
	return Credential{Email: email, Password: pass, Raw: trimmed}, true
}
