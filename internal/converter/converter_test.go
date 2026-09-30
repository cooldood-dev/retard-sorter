package converter

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseCredential(t *testing.T) {
	cases := []struct {
		line  string
		email string
		pass  string
		ok    bool
	}{
		{"a@b.com:secret", "a@b.com", "secret", true},
		{" a@b.com : secret ", "a@b.com", "secret", true},
		{"a@b.com:with:colons", "a@b.com", "with:colons", true},
		{"a@b.com:", "", "", false},
		{":secret", "", "", false},
		{"   ", "", "", false},
		{"a@b.com", "", "", false},
		{"a@b.com:pass\n", "a@b.com", "pass", true},
	}
	for _, c := range cases {
		cred, ok := ParseCredential(c.line)
		if ok != c.ok {
			t.Errorf("ParseCredential(%q) ok=%v want %v", c.line, ok, c.ok)
			continue
		}
		if ok && (cred.Email != c.email || cred.Password != c.pass) {
			t.Errorf("ParseCredential(%q) = %+v want %s/%s", c.line, cred, c.email, c.pass)
		}
	}
}

func TestNetscapeFormatting(t *testing.T) {
	out := FormatCookieFile([]Cookie{
		{Domain: ".live.com", Path: "/", Name: "MUID", Value: "abc", Secure: true, Expires: 1712345678},
		{Domain: "login.live.com", Path: "/", Name: "PPFT", Value: "xyz", HTTPOnly: true, HostOnly: true},
	})
	want := "# Netscape HTTP Cookie File\n.live.com\tFALSE\t/\tTRUE\t1712345678\tMUID\tabc\nlogin.live.com\tTRUE\t/\tFALSE\t2147483647\tPPFT\txyz\n"
	if out != want {
		t.Errorf("format mismatch:\ngot:\n%s\nwant:\n%s", out, want)
	}
}

func TestNetscapeDedupe(t *testing.T) {
	out := FormatCookieFile([]Cookie{
		{Domain: ".live.com", Path: "/", Name: "MUID", Value: "a"},
		{Domain: ".live.com", Path: "/", Name: "MUID", Value: "b"},
	})
	if n := strings.Count(out, "MUID"); n != 1 {
		t.Errorf("expected 1 MUID line, got %d", n)
	}
}

func TestNewCookieFileName(t *testing.T) {
	dir := t.TempDir()
	name, err := NewCookieFileName(dir, "retard")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(name, "retard_") || !strings.HasSuffix(name, ".txt") {
		t.Errorf("unexpected name %q", name)
	}
	core := strings.TrimSuffix(strings.TrimPrefix(name, "retard_"), ".txt")
	if len(core) < 8 || len(core) > 12 {
		t.Errorf("suffix length %d out of 8..12", len(core))
	}
	for _, r := range core {
		if !strings.ContainsRune("abcdefghijklmnopqrstuvwxyz0123456789", r) {
			t.Errorf("non-alnum char %q in suffix", r)
		}
	}
}

func TestWriteAtomic(t *testing.T) {
	dir := t.TempDir()
	if err := WriteAtomic(dir, "out.txt", []byte("data")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "out.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "data" {
		t.Errorf("got %q", data)
	}
}

func TestClassifyFailure(t *testing.T) {
	if classifyFailure("mfa") != StatusMFA || classifyFailure("captcha") != StatusCaptcha ||
		classifyFailure("locked") != StatusLocked || classifyFailure("invalid") != StatusInvalid {
		t.Error("classifyFailure mapping wrong")
	}
	if !StatusTimeout.Transient() || !StatusNetwork.Transient() || StatusMFA.Transient() || StatusInvalid.Transient() {
		t.Error("Transient() classification wrong")
	}
}
