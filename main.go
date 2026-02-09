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

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		fmt.Println("\nStopping...")
		app.Stop()
		os.Exit(0)
	}()

	fmt.Println("antifriction-trackpad started. Press Ctrl+C to stop.")
	app.Start()
}

// 慣性パラメータ
const (
	decayRate          = 5.0                   // 減衰係数 (1/sec)
	stopThreshold      = 10.0                  // 停止閾値 (px/sec)
	loopInterval       = 10 * time.Millisecond // 100Hz
	minTimeDelta       = 1e-9                  // ゼロ除算防御
	touchStateTouching = 4                     // タッチ中の state 値
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

	devices *TouchDevices
	running bool
	stop    chan struct{}
}

// NewApp は App を初期化して返す。
func NewApp() *App {
	return &App{
		stop: make(chan struct{}),
	}
}

// Start はデバイス監視を開始し、慣性ループに入る（ブロッキング）。
func (a *App) Start() {
	a.devices = OpenTouchDevices()
	a.running = true
	a.Run()
}

// Stop はデバイス監視と慣性ループを停止する。
func (a *App) Stop() {
	if a.running {
		close(a.stop)
		a.devices.Close()
		a.running = false
	}
}

// Run は 100Hz のループで慣性移動を適用する。
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

				// 指数減衰（常に 0 < factor < 1）
				factor := math.Exp(-decayRate * dt)
				a.vx *= factor
				a.vy *= factor

				if math.Sqrt(a.vx*a.vx+a.vy*a.vy) < stopThreshold {
					a.vx = 0
					a.vy = 0
				}
			}
			a.mu.Unlock()
		}
	}
}

// onTouchFrame はマルチタッチコールバックから呼ばれる。
// タッチ中はカーソル履歴を記録し、リリース時に直近2点から速度を算出する。
func (a *App) onTouchFrame(isTouched bool, timestamp float64) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if isTouched {
		x, y := getMouseLocation()
		if a.histLen < 2 {
			a.history[a.histLen] = cursorRecord{x, y, timestamp}
			a.histLen++
		} else {
			a.history[0] = a.history[1]
			a.history[1] = cursorRecord{x, y, timestamp}
		}
		a.vx = 0
		a.vy = 0
	} else if a.isTouched {
		if a.histLen >= 2 {
			s := a.history[0]
			e := a.history[1]
			dt := e.timestamp - s.timestamp
			if dt >= minTimeDelta {
				a.vx = (e.x - s.x) / dt
				a.vy = (e.y - s.y) / dt
			}
		}
		a.histLen = 0
	}

	a.isTouched = isTouched
}
