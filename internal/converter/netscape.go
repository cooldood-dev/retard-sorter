package converter

import (
	"fmt"
	"strings"
)

// MaxExpires is the far-future expiry used for session cookies without one.
const MaxExpires = 2147483647

// netscapeCookieLine renders a single cookie as a Netscape-format line:
//
//	domain\tflag\tpath\tsecure\texpires\tname\tvalue
func netscapeCookieLine(domain, path, name, value string, secure, hostOnly bool, expires int64) string {
	flag := "FALSE"
	if hostOnly {
		flag = "TRUE"
	}
	secureFlag := "FALSE"
	if secure {
		secureFlag = "TRUE"
	}
	if expires <= 0 {
		expires = MaxExpires
	}
	return strings.Join([]string{
		domain,
		flag,
		path,
		secureFlag,
		fmt.Sprintf("%d", expires),
		name,
		value,
	}, "\t")
}

// Cookie is the minimal cookie shape the writer needs, decoupled from any
// browser library types.
type Cookie struct {
	Domain    string
	Path      string
	Name      string
	Value     string
	Secure    bool
	HTTPOnly  bool
	HostOnly  bool
	Expires   int64 // Unix seconds; <= 0 means session cookie
}

// FormatCookie converts a single Cookie into a Netscape-format line.
func FormatCookie(c Cookie) string {
	return netscapeCookieLine(c.Domain, c.Path, c.Name, c.Value, c.Secure, c.HostOnly, c.Expires)
}

// FormatCookieFile renders a full Netscape cookie file from a cookie list,
// deduplicated by (domain, path, name).
func FormatCookieFile(cookies []Cookie) string {
	var b strings.Builder
	b.WriteString("# Netscape HTTP Cookie File\n")
	seen := make(map[string]bool)
	for _, c := range cookies {
		key := c.Domain + "\x00" + c.Path + "\x00" + c.Name
		if seen[key] {
			continue
		}
		seen[key] = true
		b.WriteString(netscapeCookieLine(c.Domain, c.Path, c.Name, c.Value, c.Secure, c.HostOnly, c.Expires))
		b.WriteString("\n")
	}
	return b.String()
}
