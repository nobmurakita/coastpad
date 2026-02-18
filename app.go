// app.go: App 構造体・ライフサイクル管理。
package main

import (
	"fmt"
	"sync"
	"time"
)

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

// dragPhase はドラッグ慣性の状態フェーズを表す。
// isDragCoasting / isDragFollowing / isDragPendingDecision の3つの排他的フラグを統合したもの。
type dragPhase int

const (
	dragPhaseNone            dragPhase = iota // ドラッグ慣性なし
	dragPhaseCoasting                         // ドラッグ慣性中
	dragPhaseFollowing                        // ドラッグ追従中（コースト後に複数指で再タッチ）
	dragPhasePendingDecision                  // コースト後1本指タッチ、判定保留中
)

// cursorRecord はある時点のカーソル位置を保持する。
type cursorRecord struct {
	x, y      float64
	timestamp float64
}

// App はタッチイベントの監視と慣性移動ループを管理する。
type App struct {
	mu        sync.Mutex
	history   [2]cursorRecord // 直近2点の記録（速度算出用）
	histLen   int
	isTouched bool
	vx, vy    float64 // 慣性速度 (px/sec)

	// ドラッグ慣性サポート
	// ドラッグ中に指を離すと OS がマウスアップを発行するが、これを EventTap で傍受・保留し、
	// 代わりに mouseDragged イベントを送り続けてドラッグセッションを延長する。
	// コースト完了時に保留中のマウスアップを解放してドラッグセッションを終了する。
	//
	// ドラッグ追従: コースト中に再タッチすると、移動前に複数指が検出された場合のみ
	// ドラッグ追従モードへ移行する。pendingMouseUp を保持したまま、カーソル移動を
	// mouseDragged に変換してウィンドウを追従させる。
	// 1本指で移動が検出された場合はドラッグを終了する。
	isLeftButtonDown   bool      // マウスダウン中か（EventTap で追跡）
	dragPhase          dragPhase // ドラッグ慣性の状態フェーズ
	wasMultiFingerDrag bool      // 現在のドラッグが複数指で開始されたか
	coastX, coastY     float64   // コースト中のカーソル位置追跡
	accumX, accumY     float64   // ドラッグイベント用の端数デルタ蓄積
	pendingMouseUp     eventRef  // 保留中のマウスアップ（CFRetain 済み）

	// 画面バウンドキャッシュ（コースト開始時に取得、clampToScreen で使用）
	screenMinX, screenMinY float64
	screenMaxX, screenMaxY float64

	eventTapRef     machPortRef   // タイムアウト再有効化用
	eventTapRunLoop runLoopRef    // 停止時の CFRunLoopStop 用
	eventTapDone    chan struct{} // RunLoop goroutine の終了通知

	notifier     *DeviceNotifier
	touchDevices *TouchDevices
	stopOnce     sync.Once
	stop         chan struct{}
}

// NewApp は App を初期化して返す。
func NewApp() *App {
	return &App{
		stop: make(chan struct{}),
	}
}

// Open はタッチデバイスを検出し、コールバック・EventTap・デバイス通知を登録する。
func (a *App) Open() error {
	// IOKit デバイス変更通知の開始
	notifier, err := StartDeviceNotifier()
	if err != nil {
		return fmt.Errorf("failed to start device notifier: %w", err)
	}
	a.notifier = notifier

	// タッチデバイスの初期検出とコールバック登録
	a.touchDevices = NewTouchDevices()
	a.touchDevices.RefreshDevices()

	if err := a.startEventTap(); err != nil {
		a.notifier.Stop()
		a.touchDevices.StopAll()
		return fmt.Errorf("failed to start event tap: %w", err)
	}
	return nil
}

// Stop はデバイス監視と慣性ループを停止する。
func (a *App) Stop() {
	a.stopOnce.Do(func() {
		close(a.stop)
		a.notifier.Stop()
		a.touchDevices.StopAll()
		a.stopEventTap()

		a.mu.Lock()
		pending := a.pendingMouseUp
		a.pendingMouseUp = 0
		a.mu.Unlock()
		releasePendingMouseUp(pending)
	})
}

// onDeviceChanged は IOKit 通知から呼ばれ、デバイスリストを更新する。
func (a *App) onDeviceChanged() {
	if a.touchDevices == nil {
		return
	}
	a.touchDevices.RefreshDevices()
}

// Run は慣性移動ループを実行する。Stop() が呼ばれるまでブロックする。
//
// 通常の慣性: moveMouse でカーソルを相対移動する。
// ドラッグ慣性: mouseDragged イベントを発行してドラッグセッションを延長する。
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
