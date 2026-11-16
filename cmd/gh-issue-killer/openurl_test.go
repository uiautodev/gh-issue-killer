package main

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestOpenURLCommandSelectsPlatformHelper(t *testing.T) {
	const target = "https://example.com/issue/1?a=1&b=2"
	cases := []struct {
		goos     string
		wantName string
		wantArgs []string
	}{
		{"darwin", "open", []string{target}},
		{"linux", "xdg-open", []string{target}},
		{"freebsd", "xdg-open", []string{target}},
		{"windows", "rundll32", []string{"url.dll,FileProtocolHandler", target}},
	}
	for _, tc := range cases {
		name, args := openURLCommand(tc.goos, target)
		if name != tc.wantName || !reflect.DeepEqual(args, tc.wantArgs) {
			t.Fatalf("%s = %q %q, want %q %q", tc.goos, name, args, tc.wantName, tc.wantArgs)
		}
	}
}

func TestOpenExternalURLUsesInjectedRunner(t *testing.T) {
	old := runOpenURL
	t.Cleanup(func() { runOpenURL = old })

	var gotOS, gotURL string
	runOpenURL = func(goos, rawURL string) error {
		gotOS, gotURL = goos, rawURL
		return nil
	}
	if err := openExternalURL("https://example.com"); err != nil {
		t.Fatal(err)
	}
	if gotURL != "https://example.com" || gotOS == "" {
		t.Fatalf("runner got goos=%q url=%q", gotOS, gotURL)
	}

	failure := errors.New("no browser")
	runOpenURL = func(string, string) error { return failure }
	if err := openExternalURL("https://example.com"); !errors.Is(err, failure) {
		t.Fatalf("err = %v, want %v", err, failure)
	}
}

func TestOpenURLCommandDoesNotUseShell(t *testing.T) {
	for _, goos := range []string{"darwin", "linux", "windows"} {
		name, args := openURLCommand(goos, "https://example.com/?a=1&b=2")
		if strings.Contains(name, "cmd") || strings.Contains(name, "sh") {
			t.Fatalf("%s must not go through a shell: %q", goos, name)
		}
		for _, arg := range args {
			if arg == "-c" || arg == "/c" {
				t.Fatalf("%s must not pass a shell flag: %q", goos, args)
			}
		}
	}
}
