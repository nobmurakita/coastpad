// bridge_touch_callback: MultitouchSupport の C コールバックを
// Go の goTouchCallback に中継する。C から Go の export 関数を
// 直接コールバック登録できないため、この中継関数が必要。
#include "multitouch.h"
#include "_cgo_export.h"

int bridge_touch_callback(MTDeviceRef device, Finger *data, int dataNum, double timestamp, int frame) {
    goTouchCallback(device, data, dataNum, timestamp, frame);
    return 0;
}

// --- IOKit デバイス変更通知 ---

// イテレータを排出して通知を再装填する（IOKit の通知は排出しないと次が届かない）
static void drain_iterator(io_iterator_t iter) {
    io_object_t obj;
    while ((obj = IOIteratorNext(iter)) != IO_OBJECT_NULL) {
        IOObjectRelease(obj);
    }
}

// デバイス変更通知のコールバックブリッジ
void bridge_iokit_callback(void *refcon, io_iterator_t iterator) {
    drain_iterator(iterator);
    goIOKitDeviceChanged();
}

// デバイス追加・削除の通知を登録する。
// matching dict は IOServiceAddMatchingNotification が消費するため、呼び出し側で解放不要。
kern_return_t setup_iokit_notifications(IONotificationPortRef port,
    const char *className, io_iterator_t *addIter, io_iterator_t *removeIter) {

    CFMutableDictionaryRef matchAdd = IOServiceMatching(className);
    CFMutableDictionaryRef matchRemove = IOServiceMatching(className);
    if (!matchAdd || !matchRemove) {
        if (matchAdd)  CFRelease(matchAdd);
        if (matchRemove) CFRelease(matchRemove);
        return KERN_FAILURE;
    }

    kern_return_t kr = IOServiceAddMatchingNotification(
        port, kIOFirstMatchNotification, matchAdd,
        bridge_iokit_callback, NULL, addIter);
    if (kr != KERN_SUCCESS) return kr;
    drain_iterator(*addIter);

    kr = IOServiceAddMatchingNotification(
        port, kIOTerminatedNotification, matchRemove,
        bridge_iokit_callback, NULL, removeIter);
    if (kr != KERN_SUCCESS) {
        IOObjectRelease(*addIter);
        *addIter = 0;
        return kr;
    }
    drain_iterator(*removeIter);

    return KERN_SUCCESS;
}

// 通知リソースを解放する
void cleanup_iokit_notifications(IONotificationPortRef port,
    io_iterator_t addIter, io_iterator_t removeIter) {
    if (addIter)  IOObjectRelease(addIter);
    if (removeIter) IOObjectRelease(removeIter);
    if (port) IONotificationPortDestroy(port);
}
