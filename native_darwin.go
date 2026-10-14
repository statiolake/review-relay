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

static NSMenuItem *item(NSString *title, SEL action, NSString *key, id target) {
	NSMenuItem *i = [[NSMenuItem alloc] initWithTitle:title action:action keyEquivalent:key];
	i.target = target;
	return i;
}

// Installs the menu bar macOS expects. Quit and Close stop the run loop
// (rather than terminate:, which exits on the spot) so that main can clean
// up. The Edit items have no target, so they reach the focused text field:
// without them ⌘C / ⌘V / ⌘A do nothing in the WebView.
static void installMenu(void) {
	NSMenu *bar = [[NSMenu alloc] init];

	NSMenu *app = [[NSMenu alloc] init];
	[app addItem:item(@"Quit Review Relay", @selector(stop:), @"q", NSApp)];
	NSMenuItem *appItem = [[NSMenuItem alloc] init];
	appItem.submenu = app;
	[bar addItem:appItem];

	NSMenu *file = [[NSMenu alloc] initWithTitle:@"File"];
	[file addItem:item(@"Close Window", @selector(stop:), @"w", NSApp)];
	NSMenuItem *fileItem = [[NSMenuItem alloc] init];
	fileItem.submenu = file;
	[bar addItem:fileItem];

	NSMenu *edit = [[NSMenu alloc] initWithTitle:@"Edit"];
	[edit addItem:item(@"Undo", @selector(undo:), @"z", nil)];
	NSMenuItem *redo = item(@"Redo", @selector(redo:), @"z", nil);
	redo.keyEquivalentModifierMask = NSEventModifierFlagCommand | NSEventModifierFlagShift;
	[edit addItem:redo];
	[edit addItem:[NSMenuItem separatorItem]];
	[edit addItem:item(@"Cut", @selector(cut:), @"x", nil)];
	[edit addItem:item(@"Copy", @selector(copy:), @"c", nil)];
	[edit addItem:item(@"Paste", @selector(paste:), @"v", nil)];
	[edit addItem:item(@"Select All", @selector(selectAll:), @"a", nil)];
	NSMenuItem *editItem = [[NSMenuItem alloc] init];
	editItem.submenu = edit;
	[bar addItem:editItem];

	NSApp.mainMenu = bar;
}

// Shows the app icon in the Dock and the app switcher; a bare executable
// has no bundle to carry one.
static void setIcon(const void *png, int length) {
	NSImage *image = [[NSImage alloc] initWithData:[NSData dataWithBytes:png length:length]];
	if (!image) abort(); // the embedded PNG is fixed at build time
	NSApp.applicationIconImage = image;
}
*/
import "C"

import (
	_ "embed"
	"unsafe"
)

//go:embed internal/server/assets/icon.png
var appIcon []byte

// setupNative must run on the UI thread before the page loads. The window
// colors match --bg in assets/style.css and the inline style in index.html.
func setupNative(window unsafe.Pointer) {
	C.matchBackground(window)
	C.installMenu()
	C.setIcon(unsafe.Pointer(&appIcon[0]), C.int(len(appIcon)))
}
