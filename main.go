// antifriction-trackpad: トラックパッドに慣性カーソル移動を追加する。
// 指を素早く離すとカーソルが滑り続け、指数減衰で自然に停止する。
package main

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
	decayRate          = 5.0                   // 減衰係数 (1/sec)
	stopThreshold = 10.0                  // 停止閾値 (px/sec)
	loopInterval  = 10 * time.Millisecond // 100Hz
	minTimeDelta  = 1e-9                  // ゼロ除算防御
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

// Open はタッチデバイスを検出し、コールバックを登録する。
func (a *App) Open() error {
	devices, err := OpenTouchDevices()
	if err != nil {
		return fmt.Errorf("failed to open touch devices: %w", err)
	}
	a.devices = devices
	return nil
}

// Stop はデバイス監視と慣性ループを停止する。
func (a *App) Stop() {
	a.stopOnce.Do(func() {
		close(a.stop)
		a.devices.Close()
	})
}

// Run は 100Hz のループで慣性移動を適用する。Stop() が呼ばれるまでブロックする。
func (a *App) Run() {
	ticker := time.NewTicker(loopInterval)
	defer ticker.Stop()

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
			if a.vx != 0 || a.vy != 0 {
				moveMouse(a.vx*dt, a.vy*dt)
				a.applyDecay(dt)
			}
			a.mu.Unlock()
		}
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
func (a *App) onTouchFrame(isTouched bool, timestamp float64) {
	// cgo 呼び出し（getMouseLocation）を mutex 外で実行
	var x, y float64
	var ok bool
	if isTouched {
		x, y, ok = getMouseLocation()
		if !ok {
			return
		}
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if isTouched {
		a.recordCursor(x, y, timestamp)
		a.vx = 0
		a.vy = 0
	} else if a.isTouched { // タッチ → 非タッチへの遷移（リリースエッジ）で速度を算出
		a.vx, a.vy = a.calcReleaseVelocity()
		a.histLen = 0
	}

	a.isTouched = isTouched
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
