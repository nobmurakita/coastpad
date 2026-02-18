// multitouch.c: MultitouchSupport の C コールバックを
// Go の goTouchCallback に中継する。C から Go の export 関数を
// 直接コールバック登録できないため、この中継関数が必要。
#include "multitouch.h"
#include "_cgo_export.h"

// 戻り値の型・意味はプライベート API のため不明。慣例的に 0 を返す。
int bridge_touch_callback(MTDeviceRef device, Finger *data, int dataNum, double timestamp, int frame) {
    goTouchCallback(device, data, dataNum, timestamp, frame);
    return 0;
}
