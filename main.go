// Command review-relay is a lightweight review viewer whose inline comments
// are shared live with coding agents through a loopback API and this same
// executable's agent commands.
package main

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"

	"github.com/statiolake/review-relay/internal/app"
	"github.com/statiolake/review-relay/internal/cli"
)

const usage = `Usage:
  review-relay [OPTIONS]                  view the files of the current directory
  review-relay [OPTIONS] [--] DIFF-ARGS   view a diff; DIFF-ARGS are passed to git diff

  Options:
    --devtools    enable the web inspector
    --wait        return only once the review's window has closed (macOS; the
                  window always blocks elsewhere)
    --no-window   do not open a window; print the viewer URL to open in a browser

  On macOS one resident app shows every review: the first invocation starts
  it, and each invocation opens (or brings forward) its review's window and
  returns at once.

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
	if len(args) == 1 && args[0] == app.ResidentFlag {
		return runResident()
	}
	if isAgentCommand(args) {
		return cli.Run(args, os.Stdout)
	}

	opts := map[string]bool{}
	for _, f := range cli.ViewerOptions {
		opts["--"+f.Name] = false
	}
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
	if opts["--no-window"] {
		return serve(diffArgs)
	}
	return openWindow(opts["--devtools"], opts["--wait"], diffArgs)
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

// startReview opens the review of the current directory in this process.
func startReview(diffArgs []string) (*app.Review, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	ws, _, err := app.Resolve(cwd, diffArgs, nil)
	if err != nil {
		return nil, err
	}
	return app.Start(ws)
}

// serve runs the viewer without a window until interrupted (--no-window).
func serve(diffArgs []string) error {
	r, err := startReview(diffArgs)
	if err != nil {
		return err
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	fmt.Println(r.URL())
	var fatal error
	select {
	case fatal = <-r.Failed():
	case <-signals:
	}
	return errors.Join(fatal, r.Close())
}
