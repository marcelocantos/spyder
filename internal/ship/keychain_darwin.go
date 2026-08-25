// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

//go:build darwin && cgo

package ship

/*
#cgo LDFLAGS: -framework CoreFoundation -framework Security
#include <CoreFoundation/CoreFoundation.h>
#include <Security/Security.h>
#include <stdlib.h>

// Studio secrets use SecItem* in this process only (🎯T133.1). Never
// shell out to /usr/bin/security — that would bind the ACL to the
// security CLI and any process that can invoke it.

static CFMutableDictionaryRef baseQuery(const char *service, const char *account) {
    CFMutableDictionaryRef q = CFDictionaryCreateMutable(
        kCFAllocatorDefault, 0, &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
    CFDictionarySetValue(q, kSecClass, kSecClassGenericPassword);
    CFStringRef svc = CFStringCreateWithCString(kCFAllocatorDefault, service, kCFStringEncodingUTF8);
    CFStringRef acc = CFStringCreateWithCString(kCFAllocatorDefault, account, kCFStringEncodingUTF8);
    CFDictionarySetValue(q, kSecAttrService, svc);
    CFDictionarySetValue(q, kSecAttrAccount, acc);
    CFRelease(svc);
    CFRelease(acc);
    return q;
}

static OSStatus kc_get(const char *service, const char *account, void **out, int *outLen) {
    CFMutableDictionaryRef q = baseQuery(service, account);
    CFDictionarySetValue(q, kSecReturnData, kCFBooleanTrue);
    CFDictionarySetValue(q, kSecMatchLimit, kSecMatchLimitOne);
    CFTypeRef result = NULL;
    OSStatus st = SecItemCopyMatching(q, &result);
    CFRelease(q);
    if (st != errSecSuccess) return st;
    CFDataRef data = (CFDataRef)result;
    CFIndex n = CFDataGetLength(data);
    void *buf = malloc(n);
    CFDataGetBytes(data, CFRangeMake(0, n), (UInt8 *)buf);
    CFRelease(result);
    *out = buf;
    *outLen = (int)n;
    return errSecSuccess;
}

static OSStatus kc_set(const char *service, const char *account, const void *val, int valLen) {
    CFMutableDictionaryRef q = baseQuery(service, account);
    CFDataRef data = CFDataCreate(kCFAllocatorDefault, (const UInt8 *)val, valLen);

    CFMutableDictionaryRef add = CFDictionaryCreateMutableCopy(kCFAllocatorDefault, 0, q);
    CFDictionarySetValue(add, kSecValueData, data);
    CFDictionarySetValue(add, kSecAttrLabel, CFSTR("spyder studio secrets"));
    OSStatus st = SecItemAdd(add, NULL);
    CFRelease(add);

    if (st == errSecDuplicateItem) {
        CFMutableDictionaryRef upd = CFDictionaryCreateMutable(
            kCFAllocatorDefault, 0, &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
        CFDictionarySetValue(upd, kSecValueData, data);
        st = SecItemUpdate(q, upd);
        CFRelease(upd);
    }
    CFRelease(data);
    CFRelease(q);
    return st;
}

static OSStatus kc_delete(const char *service, const char *account) {
    CFMutableDictionaryRef q = baseQuery(service, account);
    OSStatus st = SecItemDelete(q);
    CFRelease(q);
    return st;
}

static char *kc_error(OSStatus st) {
    CFStringRef msg = SecCopyErrorMessageString(st, NULL);
    if (!msg) return NULL;
    CFIndex max = CFStringGetMaximumSizeForEncoding(CFStringGetLength(msg), kCFStringEncodingUTF8) + 1;
    char *buf = malloc(max);
    CFStringGetCString(msg, buf, max, kCFStringEncodingUTF8);
    CFRelease(msg);
    return buf;
}
*/
import "C"

import (
	"fmt"
	"unsafe"
)

// errSecItemNotFound from <Security/SecBase.h>.
const errItemNotFound = -25300

func kcErrorString(st C.OSStatus) string {
	c := C.kc_error(st)
	if c == nil {
		return fmt.Sprintf("OSStatus %d", int(st))
	}
	defer C.free(unsafe.Pointer(c))
	return fmt.Sprintf("%s (OSStatus %d)", C.GoString(c), int(st))
}

// GetItem reads one generic-password blob for service/account.
// Missing item returns (nil, nil).
func GetItem(service, account string) ([]byte, error) {
	svc, acc := C.CString(service), C.CString(account)
	defer C.free(unsafe.Pointer(svc))
	defer C.free(unsafe.Pointer(acc))

	var buf unsafe.Pointer
	var n C.int
	st := C.kc_get(svc, acc, &buf, &n)
	if int(st) == errItemNotFound {
		return nil, nil
	}
	if st != C.errSecSuccess {
		return nil, fmt.Errorf("keychain read: %s", kcErrorString(st))
	}
	defer C.free(buf)
	return C.GoBytes(buf, n), nil
}

// SetItem writes (add or update) one generic-password blob.
func SetItem(service, account string, value []byte) error {
	if len(value) == 0 {
		return fmt.Errorf("keychain write: empty value")
	}
	svc, acc := C.CString(service), C.CString(account)
	defer C.free(unsafe.Pointer(svc))
	defer C.free(unsafe.Pointer(acc))

	st := C.kc_set(svc, acc, unsafe.Pointer(&value[0]), C.int(len(value)))
	if st != C.errSecSuccess {
		return fmt.Errorf("keychain write: %s", kcErrorString(st))
	}
	return nil
}

// DeleteItem removes one generic-password item. Missing is OK.
func DeleteItem(service, account string) error {
	svc, acc := C.CString(service), C.CString(account)
	defer C.free(unsafe.Pointer(svc))
	defer C.free(unsafe.Pointer(acc))

	st := C.kc_delete(svc, acc)
	if int(st) == errItemNotFound || st == C.errSecSuccess {
		return nil
	}
	return fmt.Errorf("keychain delete: %s", kcErrorString(st))
}
