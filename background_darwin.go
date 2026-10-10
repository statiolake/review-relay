package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa -framework WebKit
#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>

static WKWebView *findWebView(NSView *view) {
	if ([view isKindOfClass:[WKWebView class]]) return (WKWebView *)view;
	for (NSView *child in view.subviews) {
		WKWebView *found = findWebView(child);
		if (found) return found;
	}
	return nil;
}

// Paints the window in the page's background color (light or dark, following
// the system appearance) and stops WKWebView from drawing its own white
// background, so nothing flashes before the page's first paint.
static void matchBackground(void *window) {
	NSWindow *w = (NSWindow *)window;
	w.backgroundColor = [NSColor colorWithName:nil dynamicProvider:^NSColor *(NSAppearance *appearance) {
		NSAppearanceName name = [appearance bestMatchFromAppearancesWithNames:@[NSAppearanceNameAqua, NSAppearanceNameDarkAqua]];
		return [name isEqualToString:NSAppearanceNameDarkAqua]
			? [NSColor colorWithSRGBRed:0x17/255.0 green:0x18/255.0 blue:0x1b/255.0 alpha:1]
			: [NSColor whiteColor];
	}];
	WKWebView *webView = findWebView(w.contentView);
	if (webView) [webView setValue:@NO forKey:@"drawsBackground"];
}
*/
import "C"

import "unsafe"

// matchWindowBackground must run on the UI thread before the page loads. The
// colors match --bg in assets/style.css and the inline style in index.html.
func matchWindowBackground(window unsafe.Pointer) { C.matchBackground(window) }
