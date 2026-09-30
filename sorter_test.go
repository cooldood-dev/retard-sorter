package main

import "testing"

func TestIsPreservedAuthCookie(t *testing.T) {
	preserved := []Cookie{
		{Domain: ".login.live.com", Name: "MSPPre", Value: "x"},
		{Domain: "login.microsoftonline.com", Name: "ESTSAUTH", Value: "x"},
		{Domain: ".account.live.com", Name: "c", Value: "x"},
		{Domain: "user.auth.xboxlive.com", Name: "c", Value: "x"},
		{Domain: "api.minecraftservices.com", Name: "c", Value: "x"},
		{Domain: "login.microsoft.com", Name: "c", Value: "x"},
	}
	for _, c := range preserved {
		if !isPreservedAuthCookie(c) {
			t.Fatalf("expected preserved: %s %s", c.Domain, c.Name)
		}
	}
	notPreserved := []Cookie{
		{Domain: ".facebook.com", Name: "c", Value: "x"},
		{Domain: "spotify.com", Name: "c", Value: "x"},
		{Domain: "", Name: "c", Value: "x"},
	}
	for _, c := range notPreserved {
		if isPreservedAuthCookie(c) {
			t.Fatalf("expected NOT preserved: %s %s", c.Domain, c.Name)
		}
	}
}

func TestPreservedSuffixesCoverLoginDomains(t *testing.T) {
	// The MS checker OAuths against these hosts; their cookies must never be
	// stripped by the sorter or sorted runs silently lose accuracy.
	for _, host := range []string{
		"login.live.com",
		"login.microsoftonline.com",
		"account.live.com",
		"login.microsoft.com",
		"user.auth.xboxlive.com",
		"xsts.auth.xboxlive.com",
		"api.minecraftservices.com",
	} {
		if !isPreservedAuthCookie(Cookie{Domain: host, Name: "c", Value: "v"}) {
			t.Errorf("host %s should be preserved", host)
		}
	}
}

func TestBuildFilterIndexEmptyMatchesNothing(t *testing.T) {
	fi := buildFilterIndex(nil)
	if got := fi.match(Cookie{Domain: "spotify.com", Name: "c"}); got != "" {
		t.Fatalf("empty filters should match nothing, got %q", got)
	}
}