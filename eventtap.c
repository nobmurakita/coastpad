// eventtap.c: CGEventTap の C コールバックを Go の goEventTapCallback に中継する。
// multitouch.c と同パターン。
#include <CoreGraphics/CoreGraphics.h>
#include "_cgo_export.h"

CGEventRef bridge_event_tap_callback(CGEventTapProxy proxy, CGEventType type,
                                     CGEventRef event, void *userInfo) {
    return goEventTapCallback(proxy, type, event, userInfo);
}
