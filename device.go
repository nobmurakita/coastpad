// device.go: IOKit によるタッチデバイスの接続・切断検出。
// デバイスの変更を検出したら App に通知する。
package main

/*
#cgo LDFLAGS: -framework CoreFoundation -framework IOKit
#include "device.h"
#include <stdlib.h>
*/
import "C"
import (
	"fmt"
	"runtime"
	"sync"
	"unsafe"
)

// DeviceNotifier は IOKit 通知でタッチデバイスの接続・切断を検出する。
type DeviceNotifier struct {
	mu         sync.Mutex
	notifyPort C.IONotificationPortRef
	addIter    C.io_iterator_t
	removeIter C.io_iterator_t
	runLoop    C.CFRunLoopRef
	done       chan struct{}
}

// StartDeviceNotifier は IOKit のデバイス変更通知を開始する。
func StartDeviceNotifier() (*DeviceNotifier, error) {
	dn := &DeviceNotifier{}

	dn.notifyPort = C.IONotificationPortCreate(0) // 0 = kIOMainPortDefault
	if dn.notifyPort == nil {
		return nil, fmt.Errorf("IONotificationPortCreate failed")
	}

	if err := dn.init(); err != nil {
		dn.cleanup()
		return nil, err
	}

	// 専用ゴルーチンで RunLoop を回す（OS スレッドに固定）
	started := make(chan struct{})
	dn.done = make(chan struct{})
	go func() {
		runtime.LockOSThread()
		rl := C.CFRunLoopGetCurrent()
		dn.mu.Lock()
		dn.runLoop = rl
		dn.mu.Unlock()

		source := C.IONotificationPortGetRunLoopSource(dn.notifyPort)
		C.CFRunLoopAddSource(rl, source, C.kCFRunLoopDefaultMode)
		close(started)
		C.CFRunLoopRun()
		close(dn.done)
	}()
	<-started

	return dn, nil
}

// Stop は IOKit 通知の RunLoop を停止し、リソースを解放する。
func (dn *DeviceNotifier) Stop() {
	dn.mu.Lock()
	rl := dn.runLoop
	dn.runLoop = 0
	dn.mu.Unlock()

	if rl != 0 {
		C.CFRunLoopStop(rl)
		if dn.done != nil {
			<-dn.done
		}
	}

	dn.cleanup()
}

// init はデバイス追加・削除の IOKit 通知を登録する。
func (dn *DeviceNotifier) init() error {
	className := C.CString("AppleMultitouchDevice")
	defer C.free(unsafe.Pointer(className))

	matchAdd := C.IOServiceMatching(className)
	matchRemove := C.IOServiceMatching(className)
	if matchAdd == 0 || matchRemove == 0 {
		if matchAdd != 0 {
			C.CFRelease(C.CFTypeRef(matchAdd))
		}
		if matchRemove != 0 {
			C.CFRelease(C.CFTypeRef(matchRemove))
		}
		return fmt.Errorf("IOServiceMatching failed")
	}

	// kIOFirstMatchNotification / kIOTerminatedNotification は cgo では
	// Go 文字列定数になるため、CString で C 文字列に変換する
	addType := C.CString("IOServiceFirstMatch")
	defer C.free(unsafe.Pointer(addType))
	removeType := C.CString("IOServiceTerminate")
	defer C.free(unsafe.Pointer(removeType))

	callback := C.IOServiceMatchingCallback(C.bridge_iokit_callback)

	kr := C.IOServiceAddMatchingNotification(dn.notifyPort, addType, C.CFDictionaryRef(matchAdd), callback, nil, &dn.addIter)
	if kr != C.KERN_SUCCESS {
		return fmt.Errorf("add notification (add) failed: %d", kr)
	}
	drainIterator(dn.addIter)

	kr = C.IOServiceAddMatchingNotification(dn.notifyPort, removeType, C.CFDictionaryRef(matchRemove), callback, nil, &dn.removeIter)
	if kr != C.KERN_SUCCESS {
		C.IOObjectRelease(C.io_object_t(dn.addIter))
		dn.addIter = 0
		return fmt.Errorf("add notification (remove) failed: %d", kr)
	}
	drainIterator(dn.removeIter)

	return nil
}

// cleanup は IOKit 通知リソースを解放する。
func (dn *DeviceNotifier) cleanup() {
	if dn.addIter != 0 {
		C.IOObjectRelease(C.io_object_t(dn.addIter))
		dn.addIter = 0
	}
	if dn.removeIter != 0 {
		C.IOObjectRelease(C.io_object_t(dn.removeIter))
		dn.removeIter = 0
	}
	if dn.notifyPort != nil {
		C.IONotificationPortDestroy(dn.notifyPort)
		dn.notifyPort = nil
	}
}

// drainIterator は IOKit イテレータを排出する（排出しないと次の通知が届かない）。
func drainIterator(iter C.io_iterator_t) {
	for {
		obj := C.IOIteratorNext(iter)
		if obj == 0 {
			break
		}
		C.IOObjectRelease(obj)
	}
}

// goIOKitDeviceChanged は bridge_iokit_callback (C) から呼ばれる cgo export 関数。
// イテレータを排出して通知を再装填し、デバイスの変更を App に通知する。
//
//export goIOKitDeviceChanged
func goIOKitDeviceChanged(iterator C.uint) {
	drainIterator(C.io_iterator_t(iterator))
	if app == nil {
		return
	}
	app.onDeviceChanged()
}
