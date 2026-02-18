#ifndef DEVICE_H
#define DEVICE_H

#include <IOKit/IOKitLib.h>

// C→Go コールバックブリッジ（IOKit デバイス変更通知用）
void bridge_iokit_callback(void *refcon, io_iterator_t iterator);

#endif
