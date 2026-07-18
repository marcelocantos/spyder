// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

#include "KeychainStore.h"
#import <Foundation/Foundation.h>
#import <Security/Security.h>
#include <spdlog/spdlog.h>

namespace spyder {

static NSString* service = @"com.spyder.player";

static NSMutableDictionary* baseQuery(const std::string& key) {
    NSMutableDictionary* q = [NSMutableDictionary dictionary];
    q[(__bridge id)kSecClass] = (__bridge id)kSecClassGenericPassword;
    q[(__bridge id)kSecAttrService] = service;
    q[(__bridge id)kSecAttrAccount] = [NSString stringWithUTF8String:key.c_str()];
    return q;
}

std::string keychainLoad(const std::string& key) {
    NSMutableDictionary* q = baseQuery(key);
    q[(__bridge id)kSecReturnData] = @YES;
    q[(__bridge id)kSecMatchLimit] = (__bridge id)kSecMatchLimitOne;

    CFTypeRef result = nullptr;
    OSStatus status = SecItemCopyMatching((__bridge CFDictionaryRef)q, &result);
    if (status != errSecSuccess) return {};

    NSData* data = (__bridge_transfer NSData*)result;
    return std::string(static_cast<const char*>(data.bytes), data.length);
}

void keychainSave(const std::string& key, const std::string& value) {
    keychainDelete(key);
    NSMutableDictionary* q = baseQuery(key);
    q[(__bridge id)kSecValueData] = [NSData dataWithBytes:value.data() length:value.size()];

    OSStatus status = SecItemAdd((__bridge CFDictionaryRef)q, nullptr);
    if (status != errSecSuccess) {
        SPDLOG_WARN("Keychain save failed for '{}': {}", key, (int)status);
    }
}

void keychainDelete(const std::string& key) {
    NSMutableDictionary* q = baseQuery(key);
    SecItemDelete((__bridge CFDictionaryRef)q);
}

} // namespace spyder
