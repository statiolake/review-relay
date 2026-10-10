//go:build !darwin

package main

import "unsafe"

// matchWindowBackground is only needed on macOS, where WKWebView paints white
// before the first frame; elsewhere the page's own background is enough.
func matchWindowBackground(unsafe.Pointer) {}
