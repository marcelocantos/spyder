// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

#pragma once

#include <string>

// Keychain-backed key-value store for iOS. Values persist across app reinstalls.
namespace spyder {
std::string keychainLoad(const std::string& key);
void keychainSave(const std::string& key, const std::string& value);
void keychainDelete(const std::string& key);
}
