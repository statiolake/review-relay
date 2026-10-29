package macapp

/*
#cgo CFLAGS: -fno-objc-arc
#cgo LDFLAGS: -framework Cocoa -framework WebKit
#include <stdlib.h>
#include "macapp_darwin.h"
*/
import "C"

import (
	"strings"
	"unsafe"
)

// Handler receives what happens in the UI. WindowClosed, OpenRecent and
// Reopen are called on the main thread and must not block it;
// WillTerminate is called on the main thread right before the process
// exits and may block briefly to clean up.
type Handler interface {
	WindowClosed(id int)
	OpenRecent(i int) // -1: Clear Menu
	Reopen()
	WillTerminate()
}

var handler Handler

// Run starts the Cocoa app and never returns: quitting exits the process
// after h.WillTerminate. It must be called on the main thread (lock it in
// an init function with runtime.LockOSThread). icon is a PNG for the Dock.
func Run(h Handler, icon []byte) {
	handler = h
	C.rrRun(unsafe.Pointer(&icon[0]), C.int(len(icon)))
}

// UI implements app.UI on top of Cocoa.
type UI struct{}

func (UI) OpenWindow(id int, title, url, frameName string, devtools bool) {
	t, u, f := C.CString(title), C.CString(url), C.CString(frameName)
	defer C.free(unsafe.Pointer(t))
	defer C.free(unsafe.Pointer(u))
	defer C.free(unsafe.Pointer(f))
	d := 0
	if devtools {
		d = 1
	}
	C.rrOpenWindow(C.int(id), t, u, f, C.int(d))
}

func (UI) FocusWindow(id int) { C.rrFocusWindow(C.int(id)) }

func (UI) CloseWindow(id int) { C.rrCloseWindow(C.int(id)) }

func (UI) SetRecent(titles []string) {
	clean := make([]string, len(titles))
	for i, t := range titles {
		clean[i] = strings.ReplaceAll(t, "\n", " ")
	}
	s := C.CString(strings.Join(clean, "\n"))
	defer C.free(unsafe.Pointer(s))
	C.rrSetRecent(s)
}

func (UI) ShowError(message string) {
	s := C.CString(message)
	defer C.free(unsafe.Pointer(s))
	C.rrShowError(s)
}

func (UI) Terminate() { C.rrTerminate() }

//export goWindowClosed
func goWindowClosed(id C.int) { handler.WindowClosed(int(id)) }

//export goOpenRecent
func goOpenRecent(i C.int) { handler.OpenRecent(int(i)) }

//export goReopen
func goReopen() { handler.Reopen() }

//export goWillTerminate
func goWillTerminate() { handler.WillTerminate() }
