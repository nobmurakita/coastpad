# antifriction-trackpad

macOS トラックパッドに慣性カーソル移動を追加するツール。

トラックパッドから指を素早く離すと、カーソルが慣性で滑り続ける。指数減衰で自然に減速し停止する。

## 仕組み

1. MultitouchSupport.framework（プライベート API）でトラックパッドのタッチイベントを監視
2. 指が離れた瞬間のカーソル速度を算出
3. 100Hz のループで慣性移動を適用し、指数減衰（`e^(-5t)`）で減速

## ビルド

```bash
go build
```

## 使い方

```bash
./antifriction-trackpad
```

初回実行時にアクセシビリティ権限の許可が必要（システム設定 → プライバシーとセキュリティ → アクセシビリティ）。

Ctrl+C で終了。

## 要件

- macOS
- Go 1.23+
- トラックパッド搭載の Mac（外付け Magic Trackpad も可）
