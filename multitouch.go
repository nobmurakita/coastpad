// multitouch.go: MultitouchSupport.framework（プライベート API）経由の
// トラックパッドタッチイベント監視。C コールバックを Go に中継する。
package main

/*
#cgo LDFLAGS: -framework CoreFoundation -F/System/Library/PrivateFrameworks -framework MultitouchSupport
#include "multitouch.h"
*/
import "C"
import (
	"fmt"
	"unsafe"
)

// MTDeviceRef は MultitouchSupport のデバイスハンドル（C の void*）。
type MTDeviceRef = unsafe.Pointer

// TouchDevices はマルチタッチデバイスの検出・監視・解放を管理する。
type TouchDevices struct {
	list C.CFArrayRef   // MTDeviceCreateList で取得した配列（デバイス参照の寿命を保持）
	devs []MTDeviceRef
}

// OpenTouchDevices は接続中の全デバイスを検出し、タッチコールバックを登録して監視を開始する。
func OpenTouchDevices() (*TouchDevices, error) {
	list := C.MTDeviceCreateList()
	if list == 0 {
		return nil, fmt.Errorf("MTDeviceCreateList failed: no multitouch devices found")
	}

	count := C.CFArrayGetCount(list)
	if count == 0 {
		C.CFRelease(C.CFTypeRef(list))
		return nil, fmt.Errorf("no multitouch devices found")
	}

	devs := make([]MTDeviceRef, count)
	for i := C.CFIndex(0); i < count; i++ {
		devs[i] = MTDeviceRef(C.CFArrayGetValueAtIndex(list, i))
	}

	for _, dev := range devs {
		C.MTRegisterContactFrameCallback(C.MTDeviceRef(dev), C.MTContactCallbackFunction(C.bridge_touch_callback))
		C.MTDeviceStart(C.MTDeviceRef(dev), 0)
	}

	return &TouchDevices{list: list, devs: devs}, nil
}

// Close はコールバックを解除し、デバイス監視を停止し、デバイスリストを解放する。
func (td *TouchDevices) Close() {
	for _, dev := range td.devs {
		C.MTUnregisterContactFrameCallback(C.MTDeviceRef(dev), C.MTContactCallbackFunction(C.bridge_touch_callback))
		C.MTDeviceStop(C.MTDeviceRef(dev))
	}
	if td.list != 0 {
		C.CFRelease(C.CFTypeRef(td.list))
		td.list = 0
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
