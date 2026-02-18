// coast.go: コーストループ処理。
// ~60Hz ループの1フレーム分の慣性計算と実行。
package main

import "math"

// coastAction はコーストループの1フレームで実行するアクションを表す。
// prepareCoastFrame が mutex 内で準備し、executeCoastFrame が mutex 外で実行する。
type coastAction struct {
	moveDx, moveDy float64      // 通常の慣性移動量
	dragX, dragY   float64      // ドラッグ慣性のカーソル位置
	dragDx, dragDy int          // ドラッグイベントの整数デルタ
	isDragCoasting bool         // ドラッグ慣性フレームか
	coastEnded     bool         // コーストが今フレームで終了したか
	pending        eventRef     // 終了時に解放するマウスアップ
}

// prepareCoastFrame は mutex 内でコーストの1フレーム分の状態を計算する。
func (a *App) prepareCoastFrame(dt float64) coastAction {
	a.mu.Lock()
	defer a.mu.Unlock()

	var action coastAction
	if a.vx == 0 && a.vy == 0 {
		return action
	}

	if a.dragPhase == dragPhaseCoasting {
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
		if a.dragPhase == dragPhaseCoasting {
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
