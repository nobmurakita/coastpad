// mouse.go: CoreGraphics 経由のマウスカーソル操作。
package main

/*
#cgo LDFLAGS: -framework CoreGraphics
#include <CoreGraphics/CoreGraphics.h>
*/
import "C"

// getMouseLocation は現在のカーソル位置をスクリーン座標で返す。
func getMouseLocation() (float64, float64) {
	event := C.CGEventCreate(0)
	defer C.CFRelease(C.CFTypeRef(event))
	loc := C.CGEventGetLocation(event)
	return float64(loc.x), float64(loc.y)
}

// setMouseLocation はカーソルを指定座標に移動する。
func setMouseLocation(x, y float64) {
	point := C.CGPointMake(C.CGFloat(x), C.CGFloat(y))
	event := C.CGEventCreateMouseEvent(0, C.kCGEventMouseMoved, point, 0)
	defer C.CFRelease(C.CFTypeRef(event))
	C.CGEventPost(C.kCGHIDEventTap, event)
}

// moveMouse はカーソルを相対移動する。
func moveMouse(dx, dy float64) {
	x, y := getMouseLocation()
	setMouseLocation(x+dx, y+dy)
}
