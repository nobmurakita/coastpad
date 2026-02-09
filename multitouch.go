// multitouch.go: MultitouchSupport.framework（プライベート API）経由の
// トラックパッドタッチイベント監視。C コールバックを Go に中継する。
package main

/*
#cgo LDFLAGS: -framework CoreFoundation -F/System/Library/PrivateFrameworks -framework MultitouchSupport
#include "multitouch.h"
*/
import "C"
import "unsafe"

// MTDeviceRef は MultitouchSupport のデバイスハンドル（C の void*）。
type MTDeviceRef = unsafe.Pointer

// TouchDevices はマルチタッチデバイスの検出・監視・解放を管理する。
type TouchDevices struct {
	list C.CFArrayRef   // MTDeviceCreateList で取得した配列（デバイス参照の寿命を保持）
	devs []MTDeviceRef
}

// OpenTouchDevices は接続中の全デバイスを検出し、タッチコールバックを登録して監視を開始する。
func OpenTouchDevices() *TouchDevices {
	list := C.MTDeviceCreateList()
	count := C.CFArrayGetCount(list)

	devs := make([]MTDeviceRef, count)
	for i := C.CFIndex(0); i < count; i++ {
		devs[i] = MTDeviceRef(C.CFArrayGetValueAtIndex(list, i))
	}

	for _, dev := range devs {
		C.MTRegisterContactFrameCallback(C.MTDeviceRef(dev), C.MTContactCallbackFunction(C.bridge_touch_callback))
		C.MTDeviceStart(C.MTDeviceRef(dev), 0)
	}

	return &TouchDevices{list: list, devs: devs}
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
// タッチ中の指があるかを判定し、結果を App.onTouchFrame に渡す。
//
//export goTouchCallback
func goTouchCallback(device MTDeviceRef, data *C.Finger, dataNum C.int, timestamp C.double, frame C.int) {
	_, _ = device, frame
	if app == nil {
		return
	}

	isTouched := false
	fingers := (*[1024]C.Finger)(unsafe.Pointer(data))
	for i := 0; i < int(dataNum); i++ {
		if int(fingers[i].state) == touchStateTouching {
			isTouched = true
			break
		}
	}

	app.onTouchFrame(isTouched, float64(timestamp))
}
