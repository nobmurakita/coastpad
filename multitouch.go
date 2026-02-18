// multitouch.go: MultitouchSupport.framework によるタッチデバイスの管理とイベント処理。
// デバイスリストの取得・差分更新、コールバックの登録・解除、タッチイベントの受信を行う。
package main

/*
#cgo LDFLAGS: -F/System/Library/PrivateFrameworks -framework MultitouchSupport
#include "multitouch.h"
*/
import "C"
import (
	"fmt"
	"sync"
	"unsafe"
)

// MTDeviceRef は MultitouchSupport のデバイスハンドル（C の void*）。
type MTDeviceRef = unsafe.Pointer

// TouchDevices はタッチデバイスのリストとコールバック登録を管理する。
type TouchDevices struct {
	// mu は devs/list のスワップを保護する。RefreshDevices（IOKit RunLoop スレッド）と
	// StopAll（メインゴルーチン）の並行アクセスを安全にするために必要。
	mu   sync.Mutex
	list C.CFArrayRef            // MTDeviceCreateList で取得した配列（デバイス参照の寿命を保持）
	devs map[uintptr]MTDeviceRef // ポインタ値 → デバイス参照（差分検出用）
}

// NewTouchDevices は TouchDevices を初期化して返す。
func NewTouchDevices() *TouchDevices {
	return &TouchDevices{
		devs: make(map[uintptr]MTDeviceRef),
	}
}

// RefreshDevices は現在のデバイスリストを取得し、コールバックを再登録する。
// Open からの初回呼び出しの後は、IOKit RunLoop スレッドからのみシリアルに呼ばれる。
func (td *TouchDevices) RefreshDevices() {
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

	// 旧デバイスのコールバック解除と停止（oldList が参照を保持中）
	for _, dev := range oldDevs {
		unregisterTouchCallback(dev)
	}
	if oldList != 0 {
		C.CFRelease(C.CFTypeRef(oldList))
	}

	// 新デバイスのコールバック登録と開始
	for _, dev := range newDevs {
		registerTouchCallback(dev)
	}

	prev, active := len(oldDevs), len(newDevs)
	if active != prev {
		fmt.Printf("Touch devices: %d → %d\n", prev, active)
	}
}

// StopAll は全デバイスのコールバックを解除し、監視を停止し、リストを解放する。
func (td *TouchDevices) StopAll() {
	td.mu.Lock()
	devs := td.devs
	list := td.list
	td.devs = nil
	td.list = 0
	td.mu.Unlock()

	for _, dev := range devs {
		unregisterTouchCallback(dev)
	}
	if list != 0 {
		C.CFRelease(C.CFTypeRef(list))
	}
}

// --- コールバック登録・解除 ---

// registerTouchCallback はデバイスにタッチコールバックを登録して監視を開始する。
func registerTouchCallback(dev MTDeviceRef) {
	C.MTRegisterContactFrameCallback(C.MTDeviceRef(dev), C.MTContactCallbackFunction(C.bridge_touch_callback))
	C.MTDeviceStart(C.MTDeviceRef(dev), 0)
}

// unregisterTouchCallback はデバイスのタッチコールバックを解除して監視を停止する。
func unregisterTouchCallback(dev MTDeviceRef) {
	C.MTUnregisterContactFrameCallback(C.MTDeviceRef(dev), C.MTContactCallbackFunction(C.bridge_touch_callback))
	C.MTDeviceStop(C.MTDeviceRef(dev))
}

// --- タッチイベント処理 ---

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
