// Command review-relay is a lightweight review viewer whose inline comments
// are shared live with coding agents through a loopback API and this same
// executable's agent commands.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	webview "github.com/webview/webview_go"

	"github.com/statiolake/review-relay/internal/cli"
	"github.com/statiolake/review-relay/internal/review"
	"github.com/statiolake/review-relay/internal/server"
	"github.com/statiolake/review-relay/internal/session"
	"github.com/statiolake/review-relay/internal/workspace"
)

const usage = `Usage:
  review-relay [OPTIONS]                  view the files of the current directory
  review-relay [OPTIONS] [--] DIFF-ARGS   view a diff; DIFF-ARGS are passed to git diff

  Options:
    --devtools    enable the web inspector
    --no-window   do not open a window; print the viewer URL to open in a browser

  Examples:
    review-relay HEAD~          changes since the previous commit, including uncommitted work
    review-relay main...HEAD    changes on this branch
    review-relay --             unstaged changes (plain "git diff")

` + cli.Usage

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "review-relay:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) > 0 && slices.Contains([]string{"-h", "--help", "help"}, args[0]) {
		fmt.Println(usage)
		return nil
	}
	if isAgentCommand(args) {
		return cli.Run(args, os.Stdout)
	}

	opts := map[string]bool{"--devtools": false, "--no-window": false}
	for len(args) > 0 {
		if _, ok := opts[args[0]]; !ok {
			break
		}
		opts[args[0]] = true
		args = args[1:]
	}
	var diffArgs []string // nil: file mode
	if len(args) > 0 {
		diffArgs = args
		if args[0] == "--" {
			diffArgs = args[1:]
		}
	}
	return view(opts["--devtools"], !opts["--no-window"], diffArgs)
}

// isAgentCommand reports whether args, after the agent's global flags, start
// with an agent command.
func isAgentCommand(args []string) bool {
	for i := 0; i < len(args); i++ {
		a := strings.TrimLeft(args[i], "-")
		switch {
		case !strings.HasPrefix(args[i], "-"):
			return slices.Contains(cli.Commands, args[i])
		case a == "endpoint" || a == "workspace":
			i++ // skip the value
		case strings.HasPrefix(a, "endpoint=") || strings.HasPrefix(a, "workspace="):
		default:
			return false
		}
	}
	return false
}

func view(devtools, window bool, diffArgs []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	ws, err := workspace.Open(cwd, diffArgs)
	if err != nil {
		return err
	}
	if _, root, err := session.Find(ws.Root); err == nil && root == ws.Root {
		return fmt.Errorf("a viewer for %s is already running", ws.Root)
	}

	dataDir, err := dataDir(ws.Root)
	if err != nil {
		return err
	}
	store, err := review.Open(filepath.Join(dataDir, "state.json"))
	if err != nil {
		return fmt.Errorf("%w\nmove the file away to start with an empty review", err)
	}
	prefs, err := server.OpenPrefs(filepath.Join(dataDir, "viewer.json"))
	if err != nil {
		return fmt.Errorf("%w\nmove the file away to reset the viewer state", err)
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}

	srv := server.New(ws, store, prefs, exe)
	endpoint, serve, err := srv.Listen()
	if err != nil {
		return err
	}
	reg, err := session.Register(ws.Root, endpoint)
	if err != nil {
		return err
	}

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	if !window {
		fmt.Println(endpoint + "/")
		failed := make(chan error, 1)
		go func() { failed <- fmt.Errorf("server stopped: %w", serve()) }()
		var fatal error
		select {
		case fatal = <-failed:
		case <-signals:
		}
		return errors.Join(fatal, reg.Close())
	}

	w := webview.New(devtools)
	defer w.Destroy()
	title := "Review Relay — " + filepath.Base(ws.Root)
	if ws.DiffMode() {
		title += " — git diff " + strings.Join(diffArgs, " ")
	}
	w.SetTitle(title)
	w.SetSize(1400, 900, webview.HintNone)
	setupNative(w.Window())
	w.Navigate(endpoint + "/")

	var fatal error
	stop := func(err error) {
		w.Dispatch(func() {
			fatal = err
			w.Terminate()
		})
	}
	go func() { stop(fmt.Errorf("server stopped: %w", serve())) }()
	go func() { <-signals; stop(nil) }()
	w.Run()
	return errors.Join(fatal, reg.Close())
}

// dataDir is where a workspace's review lives, outside the workspace so that
// it never ends up in a commit.
func dataDir(root string) (string, error) {
	config, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(root))
	return filepath.Join(config, "review-relay", "workspaces", hex.EncodeToString(sum[:8])), nil
}
