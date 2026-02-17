// antifriction-trackpad: トラックパッドに慣性カーソル移動を追加する。
// 指を素早く離すとカーソルが滑り続け、指数減衰で自然に停止する。
package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
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
