//go:build !darwin

package main

import (
	"errors"
	"os"
	"os/signal"
	"syscall"

	webview "github.com/webview/webview_go"
)

// openWindow shows the review in a window of its own and returns once the
// window closes, so --wait changes nothing here.
func openWindow(devtools, _ bool, diffArgs []string) error {
	r, err := startReview(diffArgs)
	if err != nil {
		return err
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)

	w := webview.New(devtools)
	defer w.Destroy()
	w.SetTitle("Review Relay — " + r.Spec.Title())
	w.SetSize(1400, 900, webview.HintNone)
	w.Navigate(r.URL())

	var fatal error
	stop := func(err error) {
		w.Dispatch(func() {
			fatal = err
			w.Terminate()
		})
	}
	go func() {
		if err, failed := <-r.Failed(); failed {
			stop(err)
		}
	}()
	go func() { <-signals; stop(nil) }()
	w.Run()
	return errors.Join(fatal, r.Close())
}

// runResident is the macOS app; other platforms open one window per process.
func runResident() error {
	return errors.New("the resident app is only available on macOS")
}
