// bridge_touch_callback: MultitouchSupport の C コールバックを
// Go の goTouchCallback に中継する。C から Go の export 関数を
// 直接コールバック登録できないため、この中継関数が必要。
#include "multitouch.h"
#include "_cgo_export.h"

int bridge_touch_callback(MTDeviceRef device, Finger *data, int dataNum, double timestamp, int frame) {
    goTouchCallback(device, data, dataNum, timestamp, frame);
    return 0;
}
