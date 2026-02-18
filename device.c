// device.c: IOKit デバイス変更通知の C→Go コールバックブリッジ。
// Go から C 関数ポインタを直接渡せないため、この中継関数が必要。
#include "device.h"
#include "_cgo_export.h"

void bridge_iokit_callback(void *refcon, io_iterator_t iterator) {
    goIOKitDeviceChanged(iterator);
}
