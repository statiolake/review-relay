// Package macapp is the resident app's Cocoa side on macOS: the
// NSApplication run loop, one NSWindow with a WKWebView per review, the menu
// bar and the Dock icon. internal/app decides what to show; this package
// only draws it. It is empty on other platforms.
package macapp
