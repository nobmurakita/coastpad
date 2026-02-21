// drag.go: ドラッグ慣性イベント処理。
// CGEventTap コールバックから呼ばれるマウスボタンイベント処理。
package main

// onMouseDown は EventTap からのマウスダウンで呼ばれる。
func (a *App) onMouseDown() {
	a.mu.Lock()
	var pending eventRef
	var discard bool
	if a.dragPhase == dragPhaseCoasting {
		pending = a.resetCoasting()
	} else if a.pendingMouseUp != 0 {
		// ドラッグ追従中に新しい mouseDown が発生（3本指ドラッグ再開等）。
		// 保留中の古い mouseUp は Post せずに破棄する。
		// Post すると新しいドラッグセッションを壊す可能性がある。
		pending = a.pendingMouseUp
		a.pendingMouseUp = 0
		a.dragPhase = dragPhaseNone
		a.wasMultiFingerDrag = false
		a.accumX = 0
		a.accumY = 0
		discard = true
	}
	a.isLeftButtonDown = true
	a.mu.Unlock()

	if discard {
		releaseEvent(pending)
	} else {
		releasePendingMouseUp(pending)
	}
}

// handleMouseUp は EventTap からのマウスアップを処理する。
// マウスアップを消費した場合は true を返す。
//
// ドラッグ慣性中: mouseUp を保留してドラッグセッションを維持する。
// 複数指ドラッグ中かつタッチ中: onTouchFrame のリリース判定を待つため一時保留する。
// 1本指操作では mouseUp を保留しない（押し込み解除後の移動をドラッグにしない）。
func (a *App) handleMouseUp(event eventRef) (suppressed bool) {
	a.mu.Lock()

	if a.dragPhase == dragPhaseCoasting || (a.isLeftButtonDown && a.isTouched && a.wasMultiFingerDrag) {
		retainEvent(event)
		old := a.pendingMouseUp
		a.pendingMouseUp = event
		a.mu.Unlock()
		// 解放は mutex 外で実行する
		if old != 0 {
			releaseEvent(old)
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
func (a *App) resetCoasting() eventRef {
	a.dragPhase = dragPhaseNone
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
