// The Cocoa side of the resident app, compiled without ARC: objects are
// retained and released by hand. Every rr* entry point may be called from
// any Go thread and dispatches to the main queue; the go* callbacks are
// called on the main thread.

#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>
#include "macapp_darwin.h"
#include "_cgo_export.h"

@interface RRDelegate : NSObject <NSApplicationDelegate, NSWindowDelegate, WKUIDelegate> {
@public
	NSMutableDictionary *windows; // NSNumber id -> NSWindow
	NSMenu *recentMenu;
	NSPoint cascade;
}
@end

static RRDelegate *delegate;

static NSString *str(const char *s) { return [NSString stringWithUTF8String:s ? s : ""]; }

// Brings the app forward. It was started from a terminal, so the system
// would otherwise leave the terminal in front of the new window.
static void activate(void) {
#pragma clang diagnostic push
#pragma clang diagnostic ignored "-Wdeprecated-declarations"
	[NSApp activateIgnoringOtherApps:YES];
#pragma clang diagnostic pop
}

// The page's background (--bg in assets/style.css), light or dark following
// the system appearance. The window paints it, the title bar is transparent
// over it and the WKWebView draws no white background of its own, so nothing
// flashes before the first paint and the title bar reads as part of the app.
static NSColor *background(void) {
	return [NSColor colorWithName:nil dynamicProvider:^NSColor *(NSAppearance *appearance) {
		NSAppearanceName name = [appearance bestMatchFromAppearancesWithNames:@[NSAppearanceNameAqua, NSAppearanceNameDarkAqua]];
		return [name isEqualToString:NSAppearanceNameDarkAqua]
			? [NSColor colorWithSRGBRed:0x17/255.0 green:0x18/255.0 blue:0x1b/255.0 alpha:1]
			: [NSColor whiteColor];
	}];
}

@implementation RRDelegate

- (instancetype)init {
	if ((self = [super init])) {
		windows = [[NSMutableDictionary alloc] init];
		cascade = NSZeroPoint;
	}
	return self;
}

// Closing the last review leaves the app running in the Dock.
- (BOOL)applicationShouldTerminateAfterLastWindowClosed:(NSApplication *)app {
	return NO;
}

// A Dock click with no window open reopens the most recent review. With
// windows that are only minimized, AppKit restores one as usual.
- (BOOL)applicationShouldHandleReopen:(NSApplication *)app hasVisibleWindows:(BOOL)visible {
	if (windows.count == 0) {
		goReopen();
		return NO;
	}
	return YES;
}

// Every way of quitting (⌘Q, the Dock menu, logout, rrTerminate) ends here:
// Go closes all reviews before the process exits.
- (NSApplicationTerminateReply)applicationShouldTerminate:(NSApplication *)app {
	goWillTerminate();
	return NSTerminateNow;
}

- (NSNumber *)idOf:(NSWindow *)window {
	for (NSNumber *key in windows) {
		if (windows[key] == window) return key;
	}
	return nil;
}

- (void)windowWillClose:(NSNotification *)notification {
	NSWindow *window = notification.object;
	NSNumber *key = [self idOf:window];
	if (!key) return;
	[[window retain] autorelease]; // outlive the dictionary entry until the event ends
	[[key retain] autorelease];
	WKWebView *webView = (WKWebView *)window.contentView;
	[webView stopLoading];
	webView.UIDelegate = nil;
	window.delegate = nil;
	[windows removeObjectForKey:key];
	goWindowClosed(key.intValue);
}

- (void)openRecent:(NSMenuItem *)sender {
	goOpenRecent((int)sender.tag);
}

- (void)clearRecent:(id)sender {
	goOpenRecent(-1);
}

// Links that would open a new window (target=_blank) open in the browser.
- (WKWebView *)webView:(WKWebView *)webView createWebViewWithConfiguration:(WKWebViewConfiguration *)configuration forNavigationAction:(WKNavigationAction *)action windowFeatures:(WKWindowFeatures *)features {
	if (action.request.URL) [[NSWorkspace sharedWorkspace] openURL:action.request.URL];
	return nil;
}

@end

static NSMenuItem *item(NSString *title, SEL action, NSString *key, id target) {
	NSMenuItem *i = [[[NSMenuItem alloc] initWithTitle:title action:action keyEquivalent:key] autorelease];
	i.target = target;
	return i;
}

static void addSubmenu(NSMenu *bar, NSMenu *menu) {
	NSMenuItem *holder = [[[NSMenuItem alloc] init] autorelease];
	holder.submenu = menu;
	[bar addItem:holder];
}

// The menu bar macOS expects. Items without a target go to the focused
// view: without the Edit items ⌘C / ⌘V / ⌘A do nothing in the WebView, and
// Close Window reaches the key window, whose closing ends its review.
static void installMenu(void) {
	NSMenu *bar = [[[NSMenu alloc] init] autorelease];

	NSMenu *app = [[[NSMenu alloc] init] autorelease];
	[app addItem:item(@"Hide Review Relay", @selector(hide:), @"h", NSApp)];
	NSMenuItem *others = item(@"Hide Others", @selector(hideOtherApplications:), @"h", NSApp);
	others.keyEquivalentModifierMask = NSEventModifierFlagCommand | NSEventModifierFlagOption;
	[app addItem:others];
	[app addItem:item(@"Show All", @selector(unhideAllApplications:), @"", NSApp)];
	[app addItem:[NSMenuItem separatorItem]];
	[app addItem:item(@"Quit Review Relay", @selector(terminate:), @"q", NSApp)];
	addSubmenu(bar, app);

	NSMenu *file = [[[NSMenu alloc] initWithTitle:@"File"] autorelease];
	NSMenuItem *recentItem = item(@"Open Recent", nil, @"", nil);
	delegate->recentMenu = [[NSMenu alloc] initWithTitle:@"Open Recent"];
	delegate->recentMenu.autoenablesItems = NO;
	recentItem.submenu = delegate->recentMenu;
	[file addItem:recentItem];
	[file addItem:[NSMenuItem separatorItem]];
	[file addItem:item(@"Close Window", @selector(performClose:), @"w", nil)];
	addSubmenu(bar, file);

	NSMenu *edit = [[[NSMenu alloc] initWithTitle:@"Edit"] autorelease];
	[edit addItem:item(@"Undo", @selector(undo:), @"z", nil)];
	NSMenuItem *redo = item(@"Redo", @selector(redo:), @"z", nil);
	redo.keyEquivalentModifierMask = NSEventModifierFlagCommand | NSEventModifierFlagShift;
	[edit addItem:redo];
	[edit addItem:[NSMenuItem separatorItem]];
	[edit addItem:item(@"Cut", @selector(cut:), @"x", nil)];
	[edit addItem:item(@"Copy", @selector(copy:), @"c", nil)];
	[edit addItem:item(@"Paste", @selector(paste:), @"v", nil)];
	[edit addItem:item(@"Select All", @selector(selectAll:), @"a", nil)];
	addSubmenu(bar, edit);

	NSMenu *window = [[[NSMenu alloc] initWithTitle:@"Window"] autorelease];
	[window addItem:item(@"Minimize", @selector(performMiniaturize:), @"m", nil)];
	[window addItem:item(@"Zoom", @selector(performZoom:), @"", nil)];
	[window addItem:[NSMenuItem separatorItem]];
	[window addItem:item(@"Bring All to Front", @selector(arrangeInFront:), @"", NSApp)];
	addSubmenu(bar, window);
	NSApp.windowsMenu = window;

	NSApp.mainMenu = bar;
}

static void fillRecent(NSArray *titles) {
	NSMenu *menu = delegate->recentMenu;
	[menu removeAllItems];
	for (NSUInteger i = 0; i < titles.count; i++) {
		NSMenuItem *entry = item(titles[i], @selector(openRecent:), @"", delegate);
		entry.tag = (NSInteger)i;
		[menu addItem:entry];
	}
	if (titles.count > 0) [menu addItem:[NSMenuItem separatorItem]];
	NSMenuItem *clear = item(@"Clear Menu", @selector(clearRecent:), @"", delegate);
	clear.enabled = titles.count > 0;
	[menu addItem:clear];
}

void rrRun(const void *icon, int iconLength) {
	@autoreleasepool {
		[NSApplication sharedApplication];
		NSApp.activationPolicy = NSApplicationActivationPolicyRegular;
		delegate = [[RRDelegate alloc] init];
		NSApp.delegate = delegate;
		installMenu();
		fillRecent(@[]);
		// A bare executable has no bundle to carry an icon.
		NSImage *image = [[[NSImage alloc] initWithData:[NSData dataWithBytes:icon length:iconLength]] autorelease];
		if (image) NSApp.applicationIconImage = image;
	}
	[NSApp run];
}

void rrOpenWindow(int id, const char *title, const char *url, const char *frameName, int devtools) {
	@autoreleasepool {
		NSString *t = str(title), *frame = str(frameName);
		NSURL *u = [NSURL URLWithString:str(url)];
		BOOL inspect = devtools != 0;
		dispatch_async(dispatch_get_main_queue(), ^{
			NSRect rect = NSMakeRect(0, 0, 1400, 900);
			NSWindow *window = [[NSWindow alloc] initWithContentRect:rect
				styleMask:NSWindowStyleMaskTitled | NSWindowStyleMaskClosable | NSWindowStyleMaskMiniaturizable | NSWindowStyleMaskResizable
				backing:NSBackingStoreBuffered defer:NO];
			window.releasedWhenClosed = NO;
			window.title = t;
			window.titlebarAppearsTransparent = YES;
			window.backgroundColor = background();

			WKWebViewConfiguration *config = [[WKWebViewConfiguration alloc] init];
			WKPreferences *prefs = config.preferences;
			if (inspect) [prefs setValue:@YES forKey:@"developerExtrasEnabled"];
			[prefs setValue:@YES forKey:@"fullScreenEnabled"];
			[prefs setValue:@YES forKey:@"javaScriptCanAccessClipboard"];
			[prefs setValue:@YES forKey:@"DOMPasteAllowed"];
			WKWebView *webView = [[WKWebView alloc] initWithFrame:rect configuration:config];
			[config release];
			[webView setValue:@NO forKey:@"drawsBackground"];
			webView.UIDelegate = delegate;
			if (@available(macOS 13.3, *)) webView.inspectable = inspect;
			window.contentView = webView;
			[webView release];

			// The frame is remembered per review; a review without one
			// starts centered, cascading from the previous new window.
			if (![window setFrameUsingName:frame]) {
				[window center];
				delegate->cascade = [window cascadeTopLeftFromPoint:delegate->cascade];
			}
			[window setFrameAutosaveName:frame];
			window.delegate = delegate;
			delegate->windows[@(id)] = window;
			[window release];

			[webView loadRequest:[NSURLRequest requestWithURL:u]];
			[window makeKeyAndOrderFront:nil];
			activate();
		});
	}
}

void rrFocusWindow(int id) {
	dispatch_async(dispatch_get_main_queue(), ^{
		NSWindow *window = delegate->windows[@(id)];
		if (!window) return;
		if (window.miniaturized) [window deminiaturize:nil];
		[window makeKeyAndOrderFront:nil];
		activate();
	});
}

void rrCloseWindow(int id) {
	dispatch_async(dispatch_get_main_queue(), ^{
		[delegate->windows[@(id)] close]; // windowWillClose: reports it
	});
}

void rrSetRecent(const char *titles) {
	@autoreleasepool {
		NSString *joined = str(titles);
		NSArray *list = joined.length ? [joined componentsSeparatedByString:@"\n"] : @[];
		dispatch_async(dispatch_get_main_queue(), ^{ fillRecent(list); });
	}
}

void rrShowError(const char *message) {
	@autoreleasepool {
		NSString *text = str(message);
		dispatch_async(dispatch_get_main_queue(), ^{
			NSAlert *alert = [[[NSAlert alloc] init] autorelease];
			alert.alertStyle = NSAlertStyleWarning;
			alert.messageText = @"Review Relay";
			alert.informativeText = text;
			activate();
			[alert runModal];
		});
	}
}

void rrTerminate(void) {
	dispatch_async(dispatch_get_main_queue(), ^{ [NSApp terminate:nil]; });
}
