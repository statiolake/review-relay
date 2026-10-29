package main

import (
	_ "embed"
	"errors"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/statiolake/review-relay/internal/app"
	"github.com/statiolake/review-relay/internal/macapp"
)

//go:embed internal/server/assets/icon.png
var appIcon []byte

// Cocoa must run on the main thread; locking it here keeps the main
// goroutine there.
func init() { runtime.LockOSThread() }

// openWindow hands the review to the resident app, starting the app if it is
// not running, and returns at once (or, with wait, once the window closes).
func openWindow(devtools, wait bool, diffArgs []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	dir, err := app.SocketDir()
	if err != nil {
		return err
	}
	c := &app.Client{Dir: dir, Build: app.BuildID(), Spawn: app.SpawnDetached(exe, dir), Notice: os.Stderr}
	_, err = c.Invoke(app.Request{
		Dir:      cwd,
		DiffArgs: diffArgs,
		Env:      app.GitEnv(os.Environ(), cwd),
		Devtools: devtools,
		Wait:     wait,
	})
	return err
}

// runResident is the app itself, started detached by the first invocation.
// It exits quietly if another app won the race to start.
func runResident() error {
	build := app.BuildID() // before the executable can be replaced on disk
	dir, err := app.SocketDir()
	if err != nil {
		return err
	}
	inst, err := app.Acquire(dir, 3*time.Second)
	if errors.Is(err, app.ErrRunning) {
		return nil
	}
	if err != nil {
		return err
	}
	log.Printf("review-relay app started (pid %d, build %s)", os.Getpid(), build)

	var recent *app.Recent
	if config, err := os.UserConfigDir(); err == nil {
		recent, err = app.OpenRecent(filepath.Join(config, "review-relay", "app.json"))
		if err != nil {
			log.Print(err)
		}
	}
	ui := macapp.UI{}
	a := app.New(ui, build, recent)
	go inst.Serve(a.Handle)

	signal.Ignore(syscall.SIGHUP, syscall.SIGINT)
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM)
	go func() { <-signals; ui.Terminate() }()

	macapp.Run(handler{a, inst}, appIcon)
	return nil
}

// handler connects Cocoa's events to the app core, keeping the main thread
// free: closing a review stops its server, and opening one runs git.
type handler struct {
	app  *app.App
	inst *app.Instance
}

func (h handler) WindowClosed(id int) { go h.app.WindowClosed(id) }
func (h handler) OpenRecent(i int)    { go h.app.OpenRecent(i) }
func (h handler) Reopen()             { go h.app.Reopen() }
func (h handler) WillTerminate() {
	h.app.Shutdown()
	h.inst.Close()
	log.Print("review-relay app quit")
}
