// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

#pragma once
#include <string>

// Spyder player core — H.264 stream glass.
// Connects to the stream relay (spyder serve) at host:port, pairs with the
// server registered under `serverName`, decodes H.264 via VideoDecoder,
// renders via SDL, forwards input. Blocks until quit.
// Returns 0 on success, non-zero on error.
int playerCore(const std::string& host, int port,
               const std::string& serverName = "server");
