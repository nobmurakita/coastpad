// mouse.go: CoreGraphics 経由のマウスカーソル操作。
package main

/*
#cgo LDFLAGS: -framework CoreGraphics
#include <CoreGraphics/CoreGraphics.h>
*/
import "C"

// getMouseLocation は現在のカーソル位置をスクリーン座標で返す。
// CGEvent の生成に失敗した場合は ok=false を返す。
func getMouseLocation() (x, y float64, ok bool) {
	event := C.CGEventCreate(0)
	if event == 0 {
		return 0, 0, false
	}
	defer C.CFRelease(C.CFTypeRef(event))
	loc := C.CGEventGetLocation(event)
	return float64(loc.x), float64(loc.y), true
}

// setMouseLocation はカーソルを指定座標に移動する。
// CGEvent の生成に失敗した場合は何もしない。
func setMouseLocation(x, y float64) {
	point := C.CGPointMake(C.CGFloat(x), C.CGFloat(y))
	event := C.CGEventCreateMouseEvent(0, C.kCGEventMouseMoved, point, 0)
	if event == 0 {
		return
	}
	defer C.CFRelease(C.CFTypeRef(event))
	C.CGEventPost(C.kCGHIDEventTap, event)
}

// moveMouse はカーソルを相対移動する。
// getMouseLocation と setMouseLocation の間にユーザーがカーソルを動かすと
// ユーザーの移動が上書きされる可能性がある（CoreGraphics に相対移動 API がないための制約）。
// カーソル位置の取得に失敗した場合は何もしない。
func moveMouse(dx, dy float64) {
	x, y, ok := getMouseLocation()
	if !ok {
		return
	}
	setMouseLocation(x+dx, y+dy)
}
