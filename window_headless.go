//go:build !cgo

package main

import "errors"

// A build without cgo has no window (the WebView needs it): the review is
// served headless (--no-window) or to an editor frontend (attach). Editor
// frontends ship this build, which cross-compiles anywhere.
var errNoWindow = errors.New("this review-relay is built without a window (cgo disabled); use --no-window, or open the review from an editor")

func openWindow(_, _ bool, _ []string) error { return errNoWindow }

func runResident() error { return errNoWindow }
