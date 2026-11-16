// Command gh-issue-killer-desktop is a Wails 3 shell around the engine. The
// engine keeps the state machine, the daemon lock and the dashboard; this
// binary only starts it, shows it in a window and stops it on quit.
//
// It is an independent Go module so the daemon's dependency set stays at
// fsnotify. Build it from this directory with `go build .`; the release
// workflow packages the macOS .app, a Linux tarball and a Windows zip with
// desktop/build/package.sh.
package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"runtime"

	"github.com/wailsapp/wails/v3/pkg/application"
)

func main() {
	log.SetFlags(0)
	supervisor, err := newSupervisor()
	if err != nil {
		fatal(err)
	}
	url, err := supervisor.Start()
	if err != nil {
		supervisor.Stop()
		fatal(err)
	}

	app := application.New(application.Options{
		Name:        "gh-issue-killer",
		Description: "GitHub issue dispatcher",
		Mac: application.MacOptions{
			// Closing the window ends the engine; nothing keeps running in the
			// background, unlike the terminal daemon.
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
	})
	// No frontend build: the window is the engine's dashboard.
	app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:      "main",
		Title:     "gh-issue-killer",
		Width:     1320,
		Height:    900,
		MinWidth:  900,
		MinHeight: 600,
		URL:       url + "?surface=desktop",
	})
	app.OnShutdown(supervisor.Stop)
	if err := app.Run(); err != nil {
		fatal(err)
	}
}

// fatal reports a startup failure where the user can see it: stderr for a
// terminal launch, a dialog for a bundle launched from Finder.
func fatal(err error) {
	fmt.Fprintln(os.Stderr, "gh-issue-killer desktop:", err)
	message := err.Error()
	if len(message) > 600 {
		message = message[:600] + "..."
	}
	if runtime.GOOS == "darwin" {
		script := fmt.Sprintf("display dialog %q with title %q buttons {\"OK\"} default button 1",
			message, "gh-issue-killer")
		_ = exec.Command("osascript", "-e", script).Run()
	}
	os.Exit(1)
}
