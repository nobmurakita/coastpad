// eventtap.go: CGEventTap によるマウスイベント傍受。
// ドラッグ慣性中のマウスアップを保留し、慣性終了時に発行する。
package main

/*
#cgo LDFLAGS: -framework CoreGraphics
#include <CoreGraphics/CoreGraphics.h>

extern CGEventRef bridge_event_tap_callback(CGEventTapProxy proxy, CGEventType type,
                                            CGEventRef event, void *userInfo);

// createEventTap は kCGEventLeftMouseDown と kCGEventLeftMouseUp を傍受する EventTap を作成する。
static inline CFMachPortRef createEventTap() {
    CGEventMask mask = (1 << kCGEventLeftMouseDown) | (1 << kCGEventLeftMouseUp);
    return CGEventTapCreate(
        kCGSessionEventTap,
        kCGHeadInsertEventTap,
        kCGEventTapOptionDefault,
        mask,
        bridge_event_tap_callback,
        NULL
    );
}
*/
import "C"
import (
	"fmt"
	"runtime"
	"unsafe"
)

// startEventTap は CGEventTap を作成し、専用スレッドで RunLoop を回す。
func (a *App) startEventTap() error {
	tap := C.createEventTap()
	if tap == 0 {
		return fmt.Errorf("CGEventTapCreate failed (accessibility permission required)")
	}
	a.eventTapRef = tap

	source := C.CFMachPortCreateRunLoopSource(C.kCFAllocatorDefault, tap, 0)
	if source == 0 {
		C.CFRelease(C.CFTypeRef(tap))
		a.eventTapRef = 0
		return fmt.Errorf("CFMachPortCreateRunLoopSource failed")
	}

	// 専用 goroutine で RunLoop を回す（OS スレッドに固定）
	started := make(chan struct{})
	a.eventTapDone = make(chan struct{})
	go func() {
		runtime.LockOSThread()
		rl := C.CFRunLoopGetCurrent()
		a.mu.Lock()
		a.eventTapRunLoop = rl
		a.mu.Unlock()

		// CFRunLoopAddSource は内部で source を CFRetain するので、ここで CFRelease して参照を手放す
		C.CFRunLoopAddSource(rl, source, C.kCFRunLoopCommonModes)
		C.CFRelease(C.CFTypeRef(source))
		close(started)
		C.CFRunLoopRun()
		close(a.eventTapDone)
	}()
	<-started

	return nil
}

// reEnableEventTap はタイムアウトで無効化された EventTap を再有効化する。
func (a *App) reEnableEventTap() {
	a.mu.Lock()
	tap := a.eventTapRef
	a.mu.Unlock()
	if tap != 0 {
		C.CGEventTapEnable(tap, C.bool(true))
	}
}

// stopEventTap は EventTap の RunLoop を停止し、リソースを解放する。
// RunLoop goroutine の終了を待ってから tap を解放する。
func (a *App) stopEventTap() {
	a.mu.Lock()
	rl := a.eventTapRunLoop
	tap := a.eventTapRef
	done := a.eventTapDone
	a.eventTapRunLoop = 0
	a.eventTapRef = 0
	a.mu.Unlock()

	if rl != 0 {
		C.CFRunLoopStop(rl)
		if done != nil {
			<-done // RunLoop goroutine の終了を待つ
		}
	}
	if tap != 0 {
		C.CGEventTapEnable(tap, C.bool(false))
		C.CFRelease(C.CFTypeRef(tap))
	}
}

//export goEventTapCallback
func goEventTapCallback(proxy C.CGEventTapProxy, eventType C.CGEventType,
	event C.CGEventRef, userInfo unsafe.Pointer) C.CGEventRef {
	_ = proxy
	_ = userInfo

	if app == nil {
		return event
	}

	switch eventType {
	case C.kCGEventLeftMouseDown:
		app.onMouseDown()
	case C.kCGEventLeftMouseUp:
		if app.handleMouseUp(event) {
			return 0 // nil を返すとイベントが消費される
		}
	case C.kCGEventTapDisabledByTimeout:
		app.reEnableEventTap()
	}

	return event
}
