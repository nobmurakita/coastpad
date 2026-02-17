// antifriction-trackpad: トラックパッドに慣性カーソル移動を追加する。
// 指を素早く離すとカーソルが滑り続け、指数減衰で自然に停止する。
package main

/*
#include <CoreGraphics/CoreGraphics.h>
*/
import "C"
import (
	"fmt"
	"math"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

var app *App

func main() {
	app = NewApp()

	if err := app.Open(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		fmt.Println("\nStopping...")
		app.Stop()
	}()

	fmt.Println("antifriction-trackpad started. Press Ctrl+C to stop.")
	app.Run()
}

// 慣性パラメータ
const (
	decayRate     = 5.0                   // 減衰係数 (1/sec)
	stopThreshold = 10.0                  // 停止閾値 (px/sec)
	loopInterval  = 16 * time.Millisecond // ~60Hz
	minTimeDelta  = 1e-9                  // ゼロ除算防御

	// ドラッグ追従判定の移動閾値（px）。コースト中に1本指で再タッチした後、
	// この閾値を超える移動があればドラッグを終了する。
	dragFollowMovementThreshold = 3.0
)

// cursorRecord はある時点のカーソル位置を保持する。
type cursorRecord struct {
	x, y      float64
	timestamp float64
}

// App はタッチイベントの監視と慣性移動ループを管理する。
type App struct {
	mu      sync.Mutex
	history [2]cursorRecord // 直近2点の記録（速度算出用）
	histLen int
	isTouched bool
	vx, vy    float64 // 慣性速度 (px/sec)

	// ドラッグ慣性サポート
	// ドラッグ中に指を離すと OS がマウスアップを発行するが、これを EventTap で傍受・保留し、
	// 代わりに合成 mouseDragged イベントを送り続けてドラッグセッションを延長する。
	// コースト完了時に保留中のマウスアップを解放してドラッグセッションを終了する。
	//
	// ドラッグ追従: コースト中に再タッチすると、移動前に複数指が検出された場合のみ
	// ドラッグ追従モードへ移行する。pendingMouseUp を保持したまま、カーソル移動を
	// 合成 mouseDragged に変換してウィンドウを追従させる。
	// 1本指で移動が検出された場合はドラッグを終了する。
	isLeftButtonDown      bool         // マウスダウン中か（EventTap で追跡）
	isDragCoasting        bool         // ドラッグ慣性中か
	isDragFollowing       bool         // ドラッグ追従中か（コースト後に複数指で再タッチ）
	isDragPendingDecision bool         // コースト後の1本指タッチで判定保留中か
	wasMultiFingerDrag    bool         // 現在のドラッグが複数指で開始されたか
	coastX, coastY   float64      // コースト中のカーソル位置追跡
	accumX, accumY   float64      // ドラッグイベント用の端数デルタ蓄積
	pendingMouseUp   C.CGEventRef // 保留中のマウスアップ（CFRetain 済み）

	// 画面バウンドキャッシュ（コースト開始時に取得、clampToScreen で使用）
	screenMinX, screenMinY float64
	screenMaxX, screenMaxY float64

	eventTapRef     C.CFMachPortRef // タイムアウト再有効化用
	eventTapRunLoop C.CFRunLoopRef  // 停止時の CFRunLoopStop 用
	eventTapDone    chan struct{}   // RunLoop goroutine の終了通知

	devices  *TouchDevices
	stopOnce sync.Once
	stop     chan struct{}
}

// NewApp は App を初期化して返す。
func NewApp() *App {
	return &App{
		stop: make(chan struct{}),
	}
}

// Open はタッチデバイスを検出し、コールバック・EventTap を登録する。
func (a *App) Open() error {
	devices, err := OpenTouchDevices()
	if err != nil {
		return fmt.Errorf("failed to open touch devices: %w", err)
	}
	a.devices = devices

	if err := a.startEventTap(); err != nil {
		a.devices.Close()
		return fmt.Errorf("failed to start event tap: %w", err)
	}
	return nil
}

// Stop はデバイス監視と慣性ループを停止する。
func (a *App) Stop() {
	a.stopOnce.Do(func() {
		close(a.stop)
		a.devices.Close()
		a.stopEventTap()

		a.mu.Lock()
		pending := a.pendingMouseUp
		a.pendingMouseUp = 0
		a.mu.Unlock()
		releasePendingMouseUp(pending)
	})
}

// Run は慣性移動ループを実行する。Stop() が呼ばれるまでブロックする。
//
// 通常の慣性: moveMouse でカーソルを相対移動する。
// ドラッグ慣性: 合成 mouseDragged イベントを発行してドラッグセッションを延長する。
// ドラッグ慣性中は mouseUp を保留しているため、OS からはドラッグ継続中に見える。
// これにより、ウィンドウ移動とリサイズの両方が慣性で動作する。
func (a *App) Run() {
	ticker := time.NewTicker(loopInterval)
	defer ticker.Stop()

	dp := newDragPoster()
	defer dp.close()

	t1 := time.Now()

	for {
		select {
		case <-a.stop:
			return
		case <-ticker.C:
			t2 := time.Now()
			dt := t2.Sub(t1).Seconds()
			t1 = t2
			action := a.prepareCoastFrame(dt)
			a.executeCoastFrame(action, dp)
		}
	}
}

// coastAction はコーストループの1フレームで実行するアクションを表す。
// prepareCoastFrame が mutex 内で準備し、executeCoastFrame が mutex 外で実行する。
type coastAction struct {
	moveDx, moveDy float64      // 通常の慣性移動量
	dragX, dragY   float64      // ドラッグ慣性のカーソル位置
	dragDx, dragDy int          // ドラッグイベントの整数デルタ
	isDragCoasting bool         // ドラッグ慣性フレームか
	coastEnded     bool         // コーストが今フレームで終了したか
	pending        C.CGEventRef // 終了時に解放するマウスアップ
}

// prepareCoastFrame は mutex 内でコーストの1フレーム分の状態を計算する。
func (a *App) prepareCoastFrame(dt float64) coastAction {
	a.mu.Lock()
	defer a.mu.Unlock()

	var action coastAction
	if a.vx == 0 && a.vy == 0 {
		return action
	}

	if a.isDragCoasting {
		// 位置を更新し、画面端でクランプする
		prevX, prevY := a.coastX, a.coastY
		a.coastX += a.vx * dt
		a.coastY += a.vy * dt
		a.clampToScreen()

		// 実際の移動量（クランプ後）から整数デルタを抽出する
		action.dragDx, action.dragDy = a.extractIntegerDelta(a.coastX-prevX, a.coastY-prevY)

		action.dragX = a.coastX
		action.dragY = a.coastY
		action.isDragCoasting = true
	} else {
		action.moveDx = a.vx * dt
		action.moveDy = a.vy * dt
	}

	a.applyDecay(dt)
	if a.vx == 0 && a.vy == 0 {
		// 自然停止: 最終位置にカーソルを同期してからマウスアップを解放する
		if a.isDragCoasting {
			action.dragX = a.coastX
			action.dragY = a.coastY
			action.coastEnded = true
		}
		action.pending = a.resetCoasting()
	}

	return action
}

// executeCoastFrame はコーストアクションに基づき cgo 呼び出しを実行する。
func (a *App) executeCoastFrame(action coastAction, dp *dragPoster) {
	if action.isDragCoasting {
		dp.post(action.dragX, action.dragY, action.dragDx, action.dragDy)
	} else if action.moveDx != 0 || action.moveDy != 0 {
		moveMouse(action.moveDx, action.moveDy)
	}
	if action.coastEnded {
		endDragSession(action.pending, action.dragX, action.dragY)
		action.pending = 0 // 発行済み
	}
	releasePendingMouseUp(action.pending)
}

// clampToScreen はコースト中のカーソル位置を画面端にクランプする。
// クランプされた軸の速度はゼロにする。
// mu をロックした状態で呼ぶこと。
func (a *App) clampToScreen() {
	if a.coastX < a.screenMinX {
		a.coastX = a.screenMinX
		a.vx = 0
	} else if a.coastX > a.screenMaxX {
		a.coastX = a.screenMaxX
		a.vx = 0
	}
	if a.coastY < a.screenMinY {
		a.coastY = a.screenMinY
		a.vy = 0
	} else if a.coastY > a.screenMaxY {
		a.coastY = a.screenMaxY
		a.vy = 0
	}
}

// cacheScreenBounds は画面バウンドを取得してキャッシュする。
// コースト開始時に1回だけ呼ぶ。
// mu をロックした状態で呼ぶこと。
// screenBounds() は CGGetActiveDisplayList を呼ぶ cgo 呼び出しだが、
// 単純なクエリでありコールバックやブロッキングのリスクがないため mutex 内で安全に呼べる。
func (a *App) cacheScreenBounds() {
	minX, minY, maxX, maxY := screenBounds()
	a.screenMinX = minX
	a.screenMinY = minY
	// maxX/maxY はピクセル境界の外側なので -1 する
	a.screenMaxX = maxX - 1
	a.screenMaxY = maxY - 1
}

// extractIntegerDelta は端数デルタを蓄積し、整数部を抽出して返す。
// mu をロックした状態で呼ぶこと。
func (a *App) extractIntegerDelta(dx, dy float64) (int, int) {
	a.accumX += dx
	a.accumY += dy
	ix, iy := int(a.accumX), int(a.accumY)
	a.accumX -= float64(ix)
	a.accumY -= float64(iy)
	return ix, iy
}

// applyDecay は慣性速度に指数減衰を適用する。
// mu をロックした状態で呼ぶこと。
func (a *App) applyDecay(dt float64) {
	factor := math.Exp(-decayRate * dt)
	a.vx *= factor
	a.vy *= factor

	if math.Sqrt(a.vx*a.vx+a.vy*a.vy) < stopThreshold {
		a.vx = 0
		a.vy = 0
	}
}

// onTouchFrame はマルチタッチコールバックから呼ばれる。
// タッチ中はカーソル履歴を記録し、リリース時に直近2点から速度を算出する。
//
// ドラッグ追従: コースト中に複数指で再タッチするとドラッグ追従モードへ移行する。
// 合成 mouseDragged でウィンドウを追従させ、リリース時に速度があれば
// ドラッグ慣性を再開する。1本指のみの場合はドラッグを終了する。
func (a *App) onTouchFrame(fingerCount int, timestamp float64) {
	// cgo 呼び出し（getMouseLocation）を mutex 外で実行
	x, y, ok := getMouseLocation()
	if !ok {
		return
	}

	action := a.prepareTouchFrame(fingerCount, x, y, timestamp)
	a.executeTouchAction(action)
}

// touchAction はタッチフレームで実行するアクションを表す。
// prepareTouchFrame が mutex 内で準備し、executeTouchAction が mutex 外で実行する。
type touchAction struct {
	warpX, warpY       float64      // ドラッグ追従開始時のワープ先
	needWarp           bool         // カーソルワープが必要か
	syncX, syncY       float64      // ドラッグ追従の合成イベント位置
	syncDx, syncDy     int          // ドラッグ追従の整数デルタ
	needDragSync       bool         // 合成ドラッグイベントが必要か
	releaseX, releaseY float64      // ドラッグ終了時の位置
	needDragEnd        bool         // ドラッグセッションの終了が必要か（ワープ付き）
	needMouseUpOnly    bool         // mouseUp のみ発行（カーソルワープなし）
	pending            C.CGEventRef // 解放するマウスアップ
}

// prepareTouchFrame は mutex 内でタッチフレームの状態を計算する。
func (a *App) prepareTouchFrame(fingerCount int, x, y, timestamp float64) touchAction {
	a.mu.Lock()
	defer a.mu.Unlock()

	var action touchAction
	isTouched := fingerCount > 0

	if isTouched {
		// 複数指ドラッグを追跡する（1本指減少時の終了判定に使用）
		if a.isLeftButtonDown && fingerCount > 1 {
			a.wasMultiFingerDrag = true
		}

		if a.isDragCoasting {
			// コースト中に再タッチ → 慣性を停止する。
			a.isDragCoasting = false
			a.accumX = 0
			a.accumY = 0
			if fingerCount > 1 {
				// 複数指 → 即座にドラッグ追従モードへ。
				// カーソルをコースト位置にワープし、次フレームのデルタ基準にする。
				action.warpX = a.coastX
				action.warpY = a.coastY
				action.needWarp = true
				a.isDragFollowing = true
				a.recordCursor(a.coastX, a.coastY, timestamp)
			} else {
				// 1本指 → ドラッグ判定を保留する。カーソルはワープしない。
				// 後続フレームで移動を検出したらドラッグを終了し、
				// 移動前に複数指になったら追従モードへ移行する。
				a.isDragFollowing = false
				a.isDragPendingDecision = true
				a.recordCursor(x, y, timestamp)
			}
		} else if a.isDragPendingDecision {
			// ドラッグ判定保留中: 移動か複数指かで判定する。
			hasMoved := math.Abs(x-a.coastX) > dragFollowMovementThreshold ||
				math.Abs(y-a.coastY) > dragFollowMovementThreshold
			if !hasMoved && fingerCount > 1 {
				// 移動前に複数指検出 → ドラッグ追従モードへ
				action.warpX = a.coastX
				action.warpY = a.coastY
				action.needWarp = true
				a.isDragFollowing = true
				a.isDragPendingDecision = false
				a.accumX = 0
				a.accumY = 0
				a.histLen = 0
				a.recordCursor(a.coastX, a.coastY, timestamp)
			} else if hasMoved {
				// 移動検出 → コースト位置で mouseUp を発行しドラッグを終了する
				action.releaseX = a.coastX
				action.releaseY = a.coastY
				action.needMouseUpOnly = true
				action.pending = a.pendingMouseUp
				a.pendingMouseUp = 0
				a.isLeftButtonDown = false
				a.isDragPendingDecision = false
				a.recordCursor(x, y, timestamp)
			} else {
				// 判定中（1本指、移動なし）→ カーソル位置を記録のみ
				a.recordCursor(x, y, timestamp)
			}
		} else if a.wasMultiFingerDrag && fingerCount == 1 && a.pendingMouseUp != 0 {
			// 複数指ドラッグから1本指に減少 → ドラッグを終了する（macOS 標準動作）。
			action.releaseX = x
			action.releaseY = y
			action.needMouseUpOnly = true
			action.pending = a.pendingMouseUp
			a.pendingMouseUp = 0
			a.isDragFollowing = false
			a.isLeftButtonDown = false
			a.wasMultiFingerDrag = false
			a.recordCursor(x, y, timestamp)
		} else {
			// ドラッグ追従中は合成ドラッグを送りウィンドウを追従させる。
			if a.isDragFollowing && a.isTouched && a.histLen > 0 {
				last := a.history[a.histLen-1]
				action.syncDx, action.syncDy = a.extractIntegerDelta(x-last.x, y-last.y)
				if action.syncDx != 0 || action.syncDy != 0 {
					action.needDragSync = true
					action.syncX = x
					action.syncY = y
				}
			}
			a.recordCursor(x, y, timestamp)
		}
		a.vx = 0
		a.vy = 0
	} else if a.isTouched {
		// タッチ → 非タッチへの遷移（リリースエッジ）で速度を算出
		a.vx, a.vy = a.calcReleaseVelocity()
		a.histLen = 0

		if a.isDragPendingDecision {
			// コースト後の判定保留中にリリース（1本指のみだった）。
			// コースト位置で mouseUp を発行してドラッグを終了する。
			// カーソルはユーザーの現在位置にあるのでワープしない。
			// 速度があれば通常の慣性として適用される。
			action.releaseX = a.coastX
			action.releaseY = a.coastY
			action.needMouseUpOnly = true
			action.pending = a.pendingMouseUp
			a.pendingMouseUp = 0
			a.isLeftButtonDown = false
			a.isDragPendingDecision = false
		} else if a.isLeftButtonDown && (a.vx != 0 || a.vy != 0) {
			// ドラッグ中にリリース → ドラッグ慣性を開始
			a.coastX = x
			a.coastY = y
			a.accumX = 0
			a.accumY = 0
			a.isDragCoasting = true
			a.isDragFollowing = false
			a.cacheScreenBounds()
		} else if a.pendingMouseUp != 0 {
			// 速度なし、保留マウスアップがあれば現在位置で解放する。
			// releasePendingMouseUp（位置修正なし）だとイベントの元のキャプチャ位置
			// （最初のドラッグリリース時）でウィンドウが飛ぶため、
			// releasePendingMouseUpAt で現在位置に上書きする。
			action.releaseX = x
			action.releaseY = y
			action.needDragEnd = true
			action.pending = a.resetCoasting()
		}
	}

	a.isTouched = isTouched
	return action
}

// executeTouchAction はタッチアクションに基づき cgo 呼び出しを実行する。
func (a *App) executeTouchAction(action touchAction) {
	if action.needWarp {
		syncCursorViaDrag(action.warpX, action.warpY)
	}
	if action.needDragSync {
		postSyntheticDrag(action.syncX, action.syncY, action.syncDx, action.syncDy)
	}
	if action.needDragEnd {
		endDragSession(action.pending, action.releaseX, action.releaseY)
		action.pending = 0
	}
	if action.needMouseUpOnly {
		releasePendingMouseUpAt(action.pending, action.releaseX, action.releaseY)
		action.pending = 0
	}
	releasePendingMouseUp(action.pending)
}

// recordCursor はカーソル位置を履歴に追加する（直近2点を保持）。
// mu をロックした状態で呼ぶこと。
func (a *App) recordCursor(x, y, timestamp float64) {
	if a.histLen < 2 {
		a.history[a.histLen] = cursorRecord{x, y, timestamp}
		a.histLen++
	} else {
		a.history[0] = a.history[1]
		a.history[1] = cursorRecord{x, y, timestamp}
	}
}

// calcReleaseVelocity は履歴の直近2点からリリース時の速度を算出する。
// mu をロックした状態で呼ぶこと。
func (a *App) calcReleaseVelocity() (vx, vy float64) {
	if a.histLen < 2 {
		return 0, 0
	}
	prev, curr := a.history[0], a.history[1]
	dt := curr.timestamp - prev.timestamp
	if dt < minTimeDelta {
		return 0, 0
	}
	return (curr.x - prev.x) / dt, (curr.y - prev.y) / dt
}

// --- ドラッグ慣性サポート ---

// onMouseDown は EventTap からのマウスダウンで呼ばれる。
func (a *App) onMouseDown() {
	a.mu.Lock()
	var pending C.CGEventRef
	var discard bool
	if a.isDragCoasting {
		pending = a.resetCoasting()
	} else if a.pendingMouseUp != 0 {
		// ドラッグ追従中に新しい mouseDown が発生（3本指ドラッグ再開等）。
		// 保留中の古い mouseUp は Post せずに破棄する。
		// Post すると新しいドラッグセッションを壊す可能性がある。
		pending = a.pendingMouseUp
		a.pendingMouseUp = 0
		a.isDragFollowing = false
		a.isDragPendingDecision = false
		a.wasMultiFingerDrag = false
		a.accumX = 0
		a.accumY = 0
		discard = true
	}
	a.isLeftButtonDown = true
	a.mu.Unlock()

	if discard {
		discardEvent(pending)
	} else {
		releasePendingMouseUp(pending)
	}
}

// handleMouseUp は EventTap からのマウスアップを処理する。
// マウスアップを消費した場合は true を返す。
//
// ドラッグ慣性中: mouseUp を保留してドラッグセッションを維持する。
// ドラッグ中かつタッチ中: onTouchFrame のリリース判定を待つため一時保留する。
func (a *App) handleMouseUp(event C.CGEventRef) (suppressed bool) {
	a.mu.Lock()

	if a.isDragCoasting || (a.isLeftButtonDown && a.isTouched) {
		C.CFRetain(C.CFTypeRef(event))
		old := a.pendingMouseUp
		a.pendingMouseUp = event
		a.mu.Unlock()
		// CFRelease は mutex 外で実行する
		if old != 0 {
			C.CFRelease(C.CFTypeRef(old))
		}
		return true
	}

	a.isLeftButtonDown = false
	a.mu.Unlock()
	return false
}

// resetCoasting はコースト状態をリセットし、保留中のマウスアップイベントを返す。
// 返されたイベントは呼び出し側が mutex 外で releasePendingMouseUp すること。
// mu をロックした状態で呼ぶこと。
func (a *App) resetCoasting() C.CGEventRef {
	a.isDragCoasting = false
	a.isDragFollowing = false
	a.isDragPendingDecision = false
	a.wasMultiFingerDrag = false
	a.vx = 0
	a.vy = 0
	a.accumX = 0
	a.accumY = 0

	pending := a.pendingMouseUp
	a.pendingMouseUp = 0
	a.isLeftButtonDown = false

	return pending
}

// releasePendingMouseUp は保留中のマウスアップを発行・解放する。
// mutex 外で呼ぶこと。
func releasePendingMouseUp(event C.CGEventRef) {
	if event != 0 {
		C.CGEventPost(C.kCGHIDEventTap, event)
		C.CFRelease(C.CFTypeRef(event))
	}
}
