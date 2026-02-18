// mouse.go: CoreGraphics 経由のマウスカーソル操作。
package main

/*
#cgo LDFLAGS: -framework CoreGraphics
#include <CoreGraphics/CoreGraphics.h>
*/
import "C"
import (
	"fmt"
	"os"
)

// eventRef は CoreGraphics イベントの参照型。
// CGo 型を mouse.go に閉じ込め、他ファイルへの CGo 依存を防ぐ。
type eventRef = C.CGEventRef

// retainEvent はイベントの参照カウントを +1 する。
func retainEvent(event eventRef) {
	C.CFRetain(C.CFTypeRef(event))
}

// releaseEvent はイベントの参照カウントを -1 する。
func releaseEvent(event eventRef) {
	C.CFRelease(C.CFTypeRef(event))
}

// --- 基本カーソル操作 ---

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
// 慣性移動中（ユーザーが指を離している間）にのみ呼ばれることを前提としている。
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

// warpCursor はイベントを発行せずにカーソル位置を移動する。
// 入力抑制が約0.25秒発生するため、直後のユーザー操作が不要な場面でのみ使うこと。
// CGWarpMouseCursorPosition はマウスとカーソルの関連付けを一時的に解除するため、
// 使用後は reassociateMouse を呼ぶこと（endDragSession は両方を行う）。
func warpCursor(x, y float64) {
	C.CGWarpMouseCursorPosition(C.CGPointMake(C.CGFloat(x), C.CGFloat(y)))
}

// reassociateMouse はマウスとカーソルの関連付けを復元する。
// CGWarpMouseCursorPosition で解除された関連付けを戻す。
func reassociateMouse() {
	C.CGAssociateMouseAndMouseCursorPosition(C.boolean_t(1))
}

// --- イベント操作 ---

// endDragSession は保留中のマウスアップを最終位置に修正して発行し、
// カーソルをワープして関連付けを復元する。
// mouseUp の発行をワープより先に行うのは、ワープが先だとドラッグセッション中に
// カーソルジャンプが発生し、ウィンドウが二重に移動してしまうため。
// mutex 外で呼ぶこと。
func endDragSession(pending C.CGEventRef, x, y float64) {
	releasePendingMouseUpAt(pending, x, y)
	warpCursor(x, y)
	reassociateMouse()
}

// releasePendingMouseUpAt は保留中のマウスアップの位置を更新してから発行・解放する。
// コースト終了時に、元のマウスアップ位置（コースト前）をコースト最終位置に修正するために使う。
// mutex 外で呼ぶこと。
func releasePendingMouseUpAt(event C.CGEventRef, x, y float64) {
	if event != 0 {
		C.CGEventSetLocation(event, C.CGPointMake(C.CGFloat(x), C.CGFloat(y)))
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

// postSyntheticDrag はカーソル追従用の mouseDragged イベントを発行する。
// OS が mouseUp 後の再タッチを mouseMoved として送る状況で、
// ドラッグセッション維持中にウィンドウを追従させるために使う。
func postSyntheticDrag(x, y float64, dx, dy int) {
	point := C.CGPointMake(C.CGFloat(x), C.CGFloat(y))
	event := C.CGEventCreateMouseEvent(0, C.kCGEventLeftMouseDragged, point, C.kCGMouseButtonLeft)
	if event == 0 {
		return
	}
	defer C.CFRelease(C.CFTypeRef(event))
	C.CGEventSetIntegerValueField(event, C.kCGMouseEventDeltaX, C.int64_t(dx))
	C.CGEventSetIntegerValueField(event, C.kCGMouseEventDeltaY, C.int64_t(dy))
	C.CGEventSetIntegerValueField(event, C.kCGMouseEventClickState, 1)
	C.CGEventPost(C.kCGHIDEventTap, event)
}

// releasePendingMouseUp は保留中のマウスアップを発行・解放する。
// mutex 外で呼ぶこと。
func releasePendingMouseUp(event C.CGEventRef) {
	if event != 0 {
		C.CGEventPost(C.kCGHIDEventTap, event)
		C.CFRelease(C.CFTypeRef(event))
	}
}

// --- ドラッグ慣性用イベントソース ---

// dragPoster はドラッグ慣性用の mouseDragged イベントを管理する。
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
// CGEventCreateMouseEvent は source に nil（0）を受け付けるため、
// CGEventSourceCreate が失敗しても動作する。
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

// --- ディスプレイ情報 ---

// screenBounds はすべてのディスプレイの結合バウンディングボックスを返す。
func screenBounds() (minX, minY, maxX, maxY float64) {
	var count C.uint32_t
	C.CGGetActiveDisplayList(0, nil, &count)
	if count == 0 {
		return 0, 0, 1920, 1080
	}
	// 最大16ディスプレイをサポート（macOS の実用上十分な上限）
	if count > 16 {
		count = 16
	}
	var displays [16]C.CGDirectDisplayID
	C.CGGetActiveDisplayList(count, &displays[0], &count)

	bounds := C.CGDisplayBounds(displays[0])
	for i := C.uint32_t(1); i < count; i++ {
		bounds = C.CGRectUnion(bounds, C.CGDisplayBounds(displays[i]))
	}
	return float64(bounds.origin.x), float64(bounds.origin.y),
		float64(bounds.origin.x + bounds.size.width),
		float64(bounds.origin.y + bounds.size.height)
}
