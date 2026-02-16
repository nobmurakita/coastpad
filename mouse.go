// mouse.go: CoreGraphics 経由のマウスカーソル操作。
package main

/*
#cgo LDFLAGS: -framework CoreGraphics
#include <CoreGraphics/CoreGraphics.h>

// warpCursorPosition はイベントを発行せずカーソルを移動する。
CGError warpCursorPosition(CGFloat x, CGFloat y) {
	CGPoint point = CGPointMake(x, y);
	return CGWarpMouseCursorPosition(point);
}

// associateMouseCursor はマウスとカーソルの関連付けを復元する。
CGError associateMouseCursor() {
	return CGAssociateMouseAndMouseCursorPosition(true);
}

// setEventLocation はイベントの位置を設定する。
static inline void setEventLocation(CGEventRef event, CGFloat x, CGFloat y) {
	CGEventSetLocation(event, CGPointMake(x, y));
}

// getScreenBounds はすべてのディスプレイの結合バウンディングボックスを返す。
static inline void getScreenBounds(CGFloat *outMinX, CGFloat *outMinY,
                                   CGFloat *outMaxX, CGFloat *outMaxY) {
	uint32_t count = 0;
	CGGetActiveDisplayList(0, NULL, &count);
	if (count == 0) {
		*outMinX = 0; *outMinY = 0; *outMaxX = 1920; *outMaxY = 1080;
		return;
	}
	CGDirectDisplayID displays[16];
	if (count > 16) count = 16;
	CGGetActiveDisplayList(count, displays, &count);

	CGRect bounds = CGDisplayBounds(displays[0]);
	for (uint32_t i = 1; i < count; i++) {
		bounds = CGRectUnion(bounds, CGDisplayBounds(displays[i]));
	}
	*outMinX = bounds.origin.x;
	*outMinY = bounds.origin.y;
	*outMaxX = bounds.origin.x + bounds.size.width;
	*outMaxY = bounds.origin.y + bounds.size.height;
}
*/
import "C"
import (
	"fmt"
	"os"
)

// screenBounds はすべてのディスプレイの結合バウンディングボックスを返す。
func screenBounds() (minX, minY, maxX, maxY float64) {
	var cMinX, cMinY, cMaxX, cMaxY C.CGFloat
	C.getScreenBounds(&cMinX, &cMinY, &cMaxX, &cMaxY)
	return float64(cMinX), float64(cMinY), float64(cMaxX), float64(cMaxY)
}

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

// warpCursor はイベントを発行せずにカーソル位置を移動する。
// 入力抑制が約0.25秒発生するため、直後のユーザー操作が不要な場面でのみ使うこと。
// CGWarpMouseCursorPosition はマウスとカーソルの関連付けを一時的に解除するため、
// 使用後は reassociateMouse を呼ぶこと。
func warpCursor(x, y float64) {
	C.warpCursorPosition(C.CGFloat(x), C.CGFloat(y))
}

// reassociateMouse はマウスとカーソルの関連付けを復元する。
// CGWarpMouseCursorPosition で解除された関連付けを戻す。
func reassociateMouse() {
	C.associateMouseCursor()
}

// releasePendingMouseUpAt は保留中のマウスアップの位置を更新してから発行・解放する。
// コースト終了時に、元のマウスアップ位置（コースト前）をコースト最終位置に修正するために使う。
// mutex 外で呼ぶこと。
func releasePendingMouseUpAt(event C.CGEventRef, x, y float64) {
	if event != 0 {
		C.setEventLocation(event, C.CGFloat(x), C.CGFloat(y))
		C.CGEventPost(C.kCGHIDEventTap, event)
		C.CFRelease(C.CFTypeRef(event))
	}
}

// syncCursorViaDrag はドラッグイベント経由でカーソル位置を同期する。
// ゼロデルタのドラッグイベントを発行してカーソルを移動するため、
// CGWarpMouseCursorPosition のような入力抑制が発生しない。
// ドラッグセッション中（mouseUp 保留中）にカーソル位置を修正するために使う。
func syncCursorViaDrag(x, y float64) {
	point := C.CGPointMake(C.CGFloat(x), C.CGFloat(y))
	event := C.CGEventCreateMouseEvent(0, C.kCGEventLeftMouseDragged, point, C.kCGMouseButtonLeft)
	if event == 0 {
		return
	}
	defer C.CFRelease(C.CFTypeRef(event))
	C.CGEventSetIntegerValueField(event, C.kCGMouseEventDeltaX, 0)
	C.CGEventSetIntegerValueField(event, C.kCGMouseEventDeltaY, 0)
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

// dragPoster はドラッグ慣性用の合成 mouseDragged イベントを管理する。
// CGEventSource を保持し、HID レベルのボタン状態を正しく反映する。
type dragPoster struct {
	source C.CGEventSourceRef
}

func newDragPoster() *dragPoster {
	source := C.CGEventSourceCreate(C.kCGEventSourceStateHIDSystemState)
	if source == 0 {
		fmt.Fprintln(os.Stderr, "[drag] CGEventSourceCreate failed, using nil source")
	}
	return &dragPoster{source: source}
}

func (dp *dragPoster) close() {
	if dp.source != 0 {
		C.CFRelease(C.CFTypeRef(dp.source))
		dp.source = 0
	}
}

// post は指定座標に kCGEventLeftMouseDragged イベントを発行する。
// dx, dy は整数 delta。ウィンドウマネージャはこの delta でウィンドウを移動する。
func (dp *dragPoster) post(x, y float64, dx, dy int) {
	point := C.CGPointMake(C.CGFloat(x), C.CGFloat(y))
	event := C.CGEventCreateMouseEvent(dp.source, C.kCGEventLeftMouseDragged, point, C.kCGMouseButtonLeft)
	if event == 0 {
		return
	}
	defer C.CFRelease(C.CFTypeRef(event))
	// delta を整数・浮動小数点の両方で設定（参照する側がアプリによって異なる）
	C.CGEventSetIntegerValueField(event, C.kCGMouseEventDeltaX, C.int64_t(dx))
	C.CGEventSetIntegerValueField(event, C.kCGMouseEventDeltaY, C.int64_t(dy))
	C.CGEventSetDoubleValueField(event, C.kCGMouseEventDeltaX, C.double(dx))
	C.CGEventSetDoubleValueField(event, C.kCGMouseEventDeltaY, C.double(dy))
	// ドラッグ中のボタン状態と圧力を設定
	C.CGEventSetIntegerValueField(event, C.kCGMouseEventClickState, 1)
	C.CGEventSetDoubleValueField(event, C.kCGMouseEventPressure, 1.0)
	C.CGEventPost(C.kCGHIDEventTap, event)
}
