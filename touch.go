// touch.go: タッチフレーム処理。
// MultitouchSupport コールバックから呼ばれるタッチ/リリースのフレーム処理。
package main

import "math"

// onTouchFrame はマルチタッチコールバックから呼ばれる。
// タッチ中はカーソル履歴を記録し、リリース時に直近2点から速度を算出する。
//
// ドラッグ追従: コースト中に複数指で再タッチするとドラッグ追従モードへ移行する。
// mouseDragged でウィンドウを追従させ、リリース時に速度があれば
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
	syncX, syncY       float64      // ドラッグ追従のイベント位置
	syncDx, syncDy     int          // ドラッグ追従の整数デルタ
	needDragSync       bool         // ドラッグイベントの発行が必要か
	releaseX, releaseY float64      // ドラッグ終了時の位置
	needDragEnd        bool         // ドラッグセッションの終了が必要か（ワープ付き）
	needMouseUpOnly    bool         // mouseUp のみ発行（カーソルワープなし）
	pending            eventRef     // 解放するマウスアップ
}

// prepareTouchFrame は mutex 内でタッチフレームの状態を計算する。
func (a *App) prepareTouchFrame(fingerCount int, x, y, timestamp float64) touchAction {
	a.mu.Lock()
	defer a.mu.Unlock()

	var action touchAction
	isTouched := fingerCount > 0

	if isTouched {
		action = a.handleTouch(fingerCount, x, y, timestamp)
		a.vx = 0
		a.vy = 0
	} else if a.isTouched {
		action = a.handleRelease(x, y)
	}

	a.isTouched = isTouched
	return action
}

// handleTouch はタッチ中のフレームを処理する。dragPhase に応じてサブメソッドへ振り分ける。
// mu をロックした状態で呼ぶこと。
func (a *App) handleTouch(fingerCount int, x, y, timestamp float64) touchAction {
	// 複数指ドラッグを追跡する（1本指減少時の終了判定に使用）
	if a.isLeftButtonDown && fingerCount > 1 {
		a.wasMultiFingerDrag = true
	}

	switch a.dragPhase {
	case dragPhaseCoasting:
		return a.handleTouchDuringCoast(fingerCount, x, y, timestamp)
	case dragPhasePendingDecision:
		return a.handleTouchDuringPending(fingerCount, x, y, timestamp)
	default:
		return a.handleTouchDefault(fingerCount, x, y, timestamp)
	}
}

// handleTouchDuringCoast はコースト中の再タッチを処理する。
// 慣性を停止し、指の本数に応じてドラッグ追従モードか判定保留モードへ移行する。
// mu をロックした状態で呼ぶこと。
func (a *App) handleTouchDuringCoast(fingerCount int, x, y, timestamp float64) touchAction {
	var action touchAction
	a.accumX = 0
	a.accumY = 0

	if fingerCount > 1 {
		// 複数指 → 即座にドラッグ追従モードへ。
		// カーソルをコースト位置にワープし、次フレームのデルタ基準にする。
		action.warpX = a.coastX
		action.warpY = a.coastY
		action.needWarp = true
		a.dragPhase = dragPhaseFollowing
		a.recordCursor(a.coastX, a.coastY, timestamp)
	} else {
		// 1本指 → ドラッグ判定を保留する。カーソルはワープしない。
		// 後続フレームで移動を検出したらドラッグを終了し、
		// 移動前に複数指になったら追従モードへ移行する。
		a.dragPhase = dragPhasePendingDecision
		a.recordCursor(x, y, timestamp)
	}

	return action
}

// handleTouchDuringPending はドラッグ判定保留中の処理を行う。
// 移動か複数指かで、ドラッグ終了 / 追従モード移行 / 継続待機を判定する。
// mu をロックした状態で呼ぶこと。
func (a *App) handleTouchDuringPending(fingerCount int, x, y, timestamp float64) touchAction {
	var action touchAction
	hasMoved := math.Abs(x-a.coastX) > dragFollowMovementThreshold ||
		math.Abs(y-a.coastY) > dragFollowMovementThreshold

	if !hasMoved && fingerCount == 1 {
		// 判定中（1本指、移動なし）→ カーソル位置を記録のみ
		a.recordCursor(x, y, timestamp)
	} else if !hasMoved {
		// 移動前に複数指検出 → ドラッグ追従モードへ
		action.warpX = a.coastX
		action.warpY = a.coastY
		action.needWarp = true
		a.dragPhase = dragPhaseFollowing
		a.accumX = 0
		a.accumY = 0
		a.histLen = 0
		a.recordCursor(a.coastX, a.coastY, timestamp)
	} else {
		// 移動検出 → コースト位置で mouseUp を発行しドラッグを終了する
		action.releaseX = a.coastX
		action.releaseY = a.coastY
		action.needMouseUpOnly = true
		action.pending = a.pendingMouseUp
		a.pendingMouseUp = 0
		a.isLeftButtonDown = false
		a.dragPhase = dragPhaseNone
		a.recordCursor(x, y, timestamp)
	}

	return action
}

// handleTouchDefault は通常のタッチ処理を行う。
// 複数指→1本指減少によるドラッグ終了と、ドラッグ追従中のイベント発行を処理する。
// mu をロックした状態で呼ぶこと。
func (a *App) handleTouchDefault(fingerCount int, x, y, timestamp float64) touchAction {
	var action touchAction

	if a.wasMultiFingerDrag && fingerCount == 1 && a.pendingMouseUp != 0 {
		// 複数指ドラッグから1本指に減少 → ドラッグを終了する（macOS 標準動作）。
		action.releaseX = x
		action.releaseY = y
		action.needMouseUpOnly = true
		action.pending = a.pendingMouseUp
		a.pendingMouseUp = 0
		a.dragPhase = dragPhaseNone
		a.isLeftButtonDown = false
		a.wasMultiFingerDrag = false
		a.recordCursor(x, y, timestamp)
	} else {
		// ドラッグ追従中は mouseDragged を送りウィンドウを追従させる。
		if a.dragPhase == dragPhaseFollowing && a.isTouched && a.histLen > 0 {
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

	return action
}

// handleRelease はリリースエッジ（タッチ→非タッチ遷移）を処理する。
// mu をロックした状態で呼ぶこと。
func (a *App) handleRelease(x, y float64) touchAction {
	var action touchAction
	a.vx, a.vy = a.calcReleaseVelocity()
	a.histLen = 0

	switch a.dragPhase {
	case dragPhasePendingDecision:
		action = a.releaseDuringPending()
	default:
		action = a.releaseDefault(x, y)
	}

	return action
}

// releaseDuringPending はドラッグ判定保留中のリリースを処理する。
// コースト位置で mouseUp を発行してドラッグを終了する。
// カーソルはユーザーの現在位置にあるのでワープしない。
// 速度があれば通常の慣性として適用される。
// mu をロックした状態で呼ぶこと。
func (a *App) releaseDuringPending() touchAction {
	var action touchAction
	action.releaseX = a.coastX
	action.releaseY = a.coastY
	action.needMouseUpOnly = true
	action.pending = a.pendingMouseUp
	a.pendingMouseUp = 0
	a.isLeftButtonDown = false
	a.dragPhase = dragPhaseNone
	return action
}

// releaseDefault は通常のリリース処理を行う。
// ドラッグ慣性の開始、または保留マウスアップの解放を処理する。
// mu をロックした状態で呼ぶこと。
func (a *App) releaseDefault(x, y float64) touchAction {
	var action touchAction

	if a.isLeftButtonDown && (a.vx != 0 || a.vy != 0) {
		// ドラッグ中にリリース → ドラッグ慣性を開始
		a.coastX = x
		a.coastY = y
		a.accumX = 0
		a.accumY = 0
		a.dragPhase = dragPhaseCoasting
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
