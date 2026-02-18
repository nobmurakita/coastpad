// eventtap.h: CGEventTap の C コールバックブリッジ。
#ifndef EVENTTAP_H
#define EVENTTAP_H

#include <CoreGraphics/CoreGraphics.h>

// C→Go コールバックブリッジ
CGEventRef bridge_event_tap_callback(CGEventTapProxy proxy, CGEventType type,
                                     CGEventRef event, void *userInfo);

#endif
