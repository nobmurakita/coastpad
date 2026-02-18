// multitouch.go: MultitouchSupport.framework（プライベート API）経由の
// トラックパッドタッチイベント監視。C コールバックを Go に中継する。
package main

/*
#cgo LDFLAGS: -framework CoreFoundation -framework IOKit -F/System/Library/PrivateFrameworks -framework MultitouchSupport
#include "multitouch.h"
#include <stdlib.h>
*/
import "C"
import (
	"fmt"
	"sync"
	"unsafe"
)

// MTDeviceRef は MultitouchSupport のデバイスハンドル（C の void*）。
type MTDeviceRef = unsafe.Pointer

// TouchDevices はマルチタッチデバイスの検出・監視・解放を管理する。
// IOKit 通知でデバイスの接続・切断を動的に検出する。
type TouchDevices struct {
	mu   sync.Mutex
	list C.CFArrayRef            // MTDeviceCreateList で取得した配列（デバイス参照の寿命を保持）
	devs map[uintptr]MTDeviceRef // ポインタ値 → デバイス参照（差分検出用）

	// IOKit デバイス変更通知
	notifyPort C.IONotificationPortRef
	addIter    C.io_iterator_t
	removeIter C.io_iterator_t
	runLoop    C.CFRunLoopRef
	done       chan struct{}
}

// OpenTouchDevices はデバイス監視を開始する。
// 接続中のデバイスを検出してコールバックを登録する。
// デバイスが 0 台でもエラーにならない（後から接続されたデバイスを自動検出する）。
func OpenTouchDevices() (*TouchDevices, error) {
	td := &TouchDevices{
		devs: make(map[uintptr]MTDeviceRef),
	}
	changes := td.refreshDevices()
	fmt.Printf("Touch devices: %d active\n", changes.active)
	return td, nil
}

// Close はデバイス監視を停止し、リソースを解放する。
func (td *TouchDevices) Close() {
	td.stopAllDevices()
}

// deviceChanges は refreshDevices の結果を保持する。
type deviceChanges struct {
	added   int
	removed int
	active  int
}

// refreshDevices は MTDeviceCreateList で現在のデバイスリストを取得し、
// 既知リストとの差分を検出してコールバックの登録・解除を行う。
func (td *TouchDevices) refreshDevices() deviceChanges {
	newList := C.MTDeviceCreateList()

	// 新しいデバイスセットを構築
	newDevs := make(map[uintptr]MTDeviceRef)
	if newList != 0 {
		count := C.CFArrayGetCount(newList)
		for i := C.CFIndex(0); i < count; i++ {
			dev := MTDeviceRef(C.CFArrayGetValueAtIndex(newList, i))
			newDevs[uintptr(dev)] = dev
		}
	}

	td.mu.Lock()
	oldDevs := td.devs
	oldList := td.list
	td.devs = newDevs
	td.list = newList
	td.mu.Unlock()

	// 差分を計算
	var added, removed []MTDeviceRef
	for key, dev := range newDevs {
		if _, ok := oldDevs[key]; !ok {
			added = append(added, dev)
		}
	}
	for key, dev := range oldDevs {
		if _, ok := newDevs[key]; !ok {
			removed = append(removed, dev)
		}
	}

	// 削除されたデバイスのコールバック解除と停止（oldList が参照を保持中）
	for _, dev := range removed {
		C.MTUnregisterContactFrameCallback(C.MTDeviceRef(dev), C.MTContactCallbackFunction(C.bridge_touch_callback))
		C.MTDeviceStop(C.MTDeviceRef(dev))
	}
	if oldList != 0 {
		C.CFRelease(C.CFTypeRef(oldList))
	}

	// 追加されたデバイスのコールバック登録と開始
	for _, dev := range added {
		C.MTRegisterContactFrameCallback(C.MTDeviceRef(dev), C.MTContactCallbackFunction(C.bridge_touch_callback))
		C.MTDeviceStart(C.MTDeviceRef(dev), 0)
	}

	return deviceChanges{added: len(added), removed: len(removed), active: len(newDevs)}
}

// onDeviceChanged は IOKit 通知から呼ばれ、デバイスリストを更新してログを出力する。
func (td *TouchDevices) onDeviceChanged() {
	changes := td.refreshDevices()
	if changes.added > 0 {
		fmt.Printf("Touch device connected (%d active)\n", changes.active)
	}
	if changes.removed > 0 {
		fmt.Printf("Touch device disconnected (%d active)\n", changes.active)
	}
}

// stopAllDevices は全デバイスのコールバックを解除し、監視を停止し、リストを解放する。
func (td *TouchDevices) stopAllDevices() {
	td.mu.Lock()
	devs := td.devs
	list := td.list
	td.devs = nil
	td.list = 0
	td.mu.Unlock()

	for _, dev := range devs {
		C.MTUnregisterContactFrameCallback(C.MTDeviceRef(dev), C.MTContactCallbackFunction(C.bridge_touch_callback))
		C.MTDeviceStop(C.MTDeviceRef(dev))
	}
	if list != 0 {
		C.CFRelease(C.CFTypeRef(list))
	}
}

// goTouchCallback は bridge_touch_callback (C) から呼ばれる cgo export 関数。
// タッチ中の指の本数を App.onTouchFrame に渡す。
//
//export goTouchCallback
func goTouchCallback(device MTDeviceRef, data *C.Finger, dataNum C.int, timestamp C.double, frame C.int) {
	_, _ = device, frame
	if app == nil {
		return
	}
	n := countActiveFingers(data, int(dataNum))
	app.onTouchFrame(n, float64(timestamp))
}

// goIOKitDeviceChanged は bridge_iokit_callback (C) から呼ばれる cgo export 関数。
// デバイスリストの変更を処理する。
//
//export goIOKitDeviceChanged
func goIOKitDeviceChanged() {
	if app == nil || app.devices == nil {
		return
	}
	app.devices.onDeviceChanged()
}

// タッチ中の state 値（multitouch.h のタッチ状態遷移を参照）
const touchStateTouching = 4

// countActiveFingers はタッチ中（state == touchStateTouching）の指の本数を返す。
func countActiveFingers(data *C.Finger, count int) int {
	n := 0
	for _, f := range unsafe.Slice(data, count) {
		if int(f.state) == touchStateTouching {
			n++
		}
	}
	return n
}
