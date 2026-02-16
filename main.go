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
	// ドラッグ追従: コースト中に再タッチするとドラッグ追従モードに移行する。
	// pendingMouseUp を保持したまま、カーソル移動を合成 mouseDragged に変換して
	// ウィンドウを追従させる。リリース時に速度があればドラッグ慣性を再開する。
	isLeftButtonDown bool         // マウスダウン中か（EventTap で追跡）
	isDragCoasting   bool         // ドラッグ慣性中か
	coastX, coastY   float64      // コースト中のカーソル位置追跡
	accumX, accumY   float64      // ドラッグイベント用の端数デルタ蓄積
	pendingMouseUp   C.CGEventRef // 保留中のマウスアップ（CFRetain 済み）

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

			a.mu.Lock()
			var dragX, dragY float64
			var dragDx, dragDy int
			var isCoasting bool
			var moveDx, moveDy float64
			var pending C.CGEventRef
			var coastEnded bool // コーストが今フレームで終了したか

			if a.vx != 0 || a.vy != 0 {
				if a.isDragCoasting {
					// 位置を更新し、画面端でクランプする
					prevX, prevY := a.coastX, a.coastY
					a.coastX += a.vx * dt
					a.coastY += a.vy * dt
					a.clampToScreen()

					// 実際の移動量（クランプ後）を端数デルタに蓄積し、整数部を抽出する
					a.accumX += a.coastX - prevX
					a.accumY += a.coastY - prevY
					dragDx = int(a.accumX)
					dragDy = int(a.accumY)
					a.accumX -= float64(dragDx)
					a.accumY -= float64(dragDy)

					dragX = a.coastX
					dragY = a.coastY
					isCoasting = true
				} else {
					moveDx = a.vx * dt
					moveDy = a.vy * dt
				}
				a.applyDecay(dt)
				if a.vx == 0 && a.vy == 0 {
					// 自然停止: 最終位置にカーソルを同期してからマウスアップを解放する
					if a.isDragCoasting {
						dragX = a.coastX
						dragY = a.coastY
						coastEnded = true
					}
					pending = a.resetCoasting()
				}
			}
			a.mu.Unlock()

			// cgo 呼び出しは mutex 外で実行
			if isCoasting {
				dp.post(dragX, dragY, dragDx, dragDy)
			} else {
				if moveDx != 0 || moveDy != 0 {
					moveMouse(moveDx, moveDy)
				}
			}
			if coastEnded {
				// ドラッグセッション終了 → カーソルワープの順で処理する。
				// ワープを先にするとドラッグセッション中のカーソルジャンプで
				// ウィンドウが二重に移動してしまう。
				releasePendingMouseUpAt(pending, dragX, dragY)
				pending = 0 // 発行済み
				warpCursor(dragX, dragY)
				reassociateMouse()
			}
			releasePendingMouseUp(pending)
		}
	}
}

// clampToScreen はコースト中のカーソル位置を画面端にクランプする。
// クランプされた軸の速度はゼロにする。
// mu をロックした状態で呼ぶこと。
func (a *App) clampToScreen() {
	minX, minY, maxX, maxY := screenBounds()
	// maxX/maxY はピクセル境界の外側なので -1 する
	maxX--
	maxY--
	if a.coastX < minX {
		a.coastX = minX
		a.vx = 0
	} else if a.coastX > maxX {
		a.coastX = maxX
		a.vx = 0
	}
	if a.coastY < minY {
		a.coastY = minY
		a.vy = 0
	} else if a.coastY > maxY {
		a.coastY = maxY
		a.vy = 0
	}
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
// ドラッグ追従: コースト中に再タッチすると慣性を停止してドラッグ追従モードへ移行する。
// 合成 mouseDragged でウィンドウを追従させ、リリース時に速度があれば
// ドラッグ慣性を再開する。
func (a *App) onTouchFrame(fingerCount int, timestamp float64) {
	isTouched := fingerCount > 0

	// cgo 呼び出し（getMouseLocation）を mutex 外で実行
	x, y, ok := getMouseLocation()
	if !ok {
		return
	}

	a.mu.Lock()
	var pending C.CGEventRef
	var warpX, warpY float64
	var needWarp bool
	var syncX, syncY float64
	var syncDx, syncDy int
	var needDragSync bool
	var releaseX, releaseY float64
	var needDragEnd bool

	if isTouched {
		if a.isDragCoasting {
			// コースト中に再タッチ → 慣性を停止しドラッグ追従モードへ移行する。
			// pendingMouseUp を保持してドラッグセッションを維持する。
			// カーソルをコースト位置にワープし、次フレームのデルタ基準にする。
			warpX = a.coastX
			warpY = a.coastY
			needWarp = true
			a.isDragCoasting = false
			a.accumX = 0
			a.accumY = 0
			a.recordCursor(warpX, warpY, timestamp)
		} else {
			// ドラッグ追従: pendingMouseUp 保留中は合成ドラッグを送り
			// ウィンドウを追従させる。
			if a.pendingMouseUp != 0 && a.isTouched && a.histLen > 0 {
				last := a.history[a.histLen-1]
				a.accumX += x - last.x
				a.accumY += y - last.y
				syncDx = int(a.accumX)
				syncDy = int(a.accumY)
				a.accumX -= float64(syncDx)
				a.accumY -= float64(syncDy)
				if syncDx != 0 || syncDy != 0 {
					needDragSync = true
					syncX = x
					syncY = y
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

		if a.isLeftButtonDown && (a.vx != 0 || a.vy != 0) {
			// ドラッグ中にリリース → ドラッグ慣性を開始
			a.coastX = x
			a.coastY = y
			a.accumX = 0
			a.accumY = 0
			a.isDragCoasting = true
		} else if a.pendingMouseUp != 0 {
			// 速度なし、保留マウスアップがあれば現在位置で解放する。
			// releasePendingMouseUp（位置修正なし）だとイベントの元のキャプチャ位置
			// （最初のドラッグリリース時）でウィンドウが飛ぶため、
			// releasePendingMouseUpAt で現在位置に上書きする。
			releaseX = x
			releaseY = y
			needDragEnd = true
			pending = a.resetCoasting()
		}
	}

	a.isTouched = isTouched
	a.mu.Unlock()

	if needWarp {
		syncCursorViaDrag(warpX, warpY)
	}
	if needDragSync {
		postSyntheticDrag(syncX, syncY, syncDx, syncDy)
	}
	if needDragEnd {
		releasePendingMouseUpAt(pending, releaseX, releaseY)
		pending = 0
		warpCursor(releaseX, releaseY)
		reassociateMouse()
	}
	releasePendingMouseUp(pending)
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
		if a.pendingMouseUp != 0 {
			C.CFRelease(C.CFTypeRef(a.pendingMouseUp))
		}
		a.pendingMouseUp = event
		a.mu.Unlock()
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

