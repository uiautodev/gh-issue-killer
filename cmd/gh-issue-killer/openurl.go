package main

import (
	"os/exec"
	"runtime"
)

// openURLCommand picks the platform helper that hands a URL to the default
// browser. It returns the executable and its arguments; nothing is passed
// through a shell, so a query string cannot inject a command.
func openURLCommand(goos, rawURL string) (string, []string) {
	switch goos {
	case "darwin":
		return "open", []string{rawURL}
	case "windows":
		// Not `cmd /c start`: cmd re-parses an unquoted `&` in the query
		// string as a command separator, and the URL comes from page content.
		return "rundll32", []string{"url.dll,FileProtocolHandler", rawURL}
	default:
		return "xdg-open", []string{rawURL}
	}
}

// runOpenURL is a variable so tests can record the command instead of
// launching a browser on the machine running them.
var runOpenURL = func(goos, rawURL string) error {
	name, args := openURLCommand(goos, rawURL)
	return exec.Command(name, args...).Start()
}

// openExternalURL opens an http(s) URL in the user's default browser.
func openExternalURL(rawURL string) error {
	return runOpenURL(runtime.GOOS, rawURL)
}
