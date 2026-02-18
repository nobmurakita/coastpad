#ifndef MULTITOUCH_H
#define MULTITOUCH_H

#include <CoreFoundation/CoreFoundation.h>
#include <IOKit/IOKitLib.h>

// MultitouchSupport.framework 構造体定義（プライベート API）
//
// タッチの状態遷移:
//   0:NotTracking → 1:StartInRange → 2:HoverInRange → 3:MakeTouch
//   → 4:Touching → 5:BreakTouch → 6:LingerInRange → 7:OutOfRange
//
// Go 側の定数: multitouch.go の touchStateTouching

// 2D 座標
typedef struct {
    float x;
    float y;
} MTPoint;

// 位置と速度のペア
typedef struct {
    MTPoint position;
    MTPoint velocity;
} MTVector;

// 1本の指のタッチ情報。コールバックで配列として渡される。
typedef struct {
    int32_t  frame;           // フレーム番号
    double   timestamp;       // イベント時刻
    int32_t  pathIndex;       // パスインデックス
    int32_t  state;           // タッチ状態 (0-7、上記の遷移を参照)
    int32_t  fingerID;        // 指の一意識別子
    int32_t  handID;          // 手の識別子（常に 1）
    MTVector normalized;      // 正規化座標（0〜1、原点は左下）と速度
    float    zTotal;          // 接触品質（1/8 の倍数、0〜1）
    float    zPressure;       // Force Touch 圧力（非 Force Touch では 0）
    float    angle;           // 接触楕円の回転角度
    float    majorAxis;       // 接触楕円の長軸
    float    minorAxis;       // 接触楕円の短軸
    MTVector absolute;        // 絶対座標（mm 単位、原点は左下）と速度
    int32_t  field14;         // 不明（常に 0）
    int32_t  field15;         // 不明（常に 0）
    float    zDensity;        // 接触面積の密度
} Finger;

typedef void *MTDeviceRef;
typedef int (*MTContactCallbackFunction)(MTDeviceRef, Finger *, int, double, int);

// MultitouchSupport extern 宣言
extern CFArrayRef MTDeviceCreateList(void);
extern void MTRegisterContactFrameCallback(MTDeviceRef, MTContactCallbackFunction);
extern void MTUnregisterContactFrameCallback(MTDeviceRef, MTContactCallbackFunction);
extern void MTDeviceStart(MTDeviceRef, int);
extern void MTDeviceStop(MTDeviceRef);

// C→Go コールバックブリッジ
int bridge_touch_callback(MTDeviceRef device, Finger *data, int dataNum, double timestamp, int frame);

// IOKit デバイス変更通知
void bridge_iokit_callback(void *refcon, io_iterator_t iterator);
kern_return_t setup_iokit_notifications(IONotificationPortRef port,
    const char *className, io_iterator_t *addIter, io_iterator_t *removeIter);
void cleanup_iokit_notifications(IONotificationPortRef port,
    io_iterator_t addIter, io_iterator_t removeIter);

#endif
