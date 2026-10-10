//go:build !darwin

package main

import "unsafe"

// setupNative is only needed on macOS, where WKWebView paints white before
// the first frame and the app has no menu bar (so no ⌘Q, ⌘C, ⌘V) unless one
// is installed; elsewhere the toolkit provides both.
func setupNative(unsafe.Pointer) {}
