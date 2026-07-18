// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0
//
// Stream host packaging policy used by DirectRenderHost under SPYDER_SERVER_BUILD
// and by the player for durable SP2T state (🎯T154).
// Kept header-only so unit tests drive the same functions as production.

#pragma once

#include <sqlite3.h>

#include <cctype>
#include <cstdint>
#include <cstring>
#include <span>
#include <string>
#include <vector>

namespace spyder {

// Durable Context::db() path on the *game host* process.
// serverBuild true  → always :memory: (working set only; player is authority)
// serverBuild false → PrefPath + "game.db" when org/app/pref provided
inline std::string durableDbPathForHost(bool serverBuild,
                                        const char* orgName,
                                        const char* appName,
                                        const char* prefPathOrNull) {
    if (serverBuild) return ":memory:";
    if (!orgName || !appName || !prefPathOrNull || !*prefPathOrNull)
        return ":memory:";
    return std::string(prefPathOrNull) + "game.db";
}

// Sanitize a game id for use as a path segment (server catalogue name / appName).
// Keeps [A-Za-z0-9._-]; everything else → '_'. Empty input → empty string.
inline std::string sanitizeGameId(const char* gameId) {
    if (!gameId || !*gameId) return {};
    std::string out;
    out.reserve(std::strlen(gameId));
    for (const char* p = gameId; *p; ++p) {
        const unsigned char c = static_cast<unsigned char>(*p);
        if (std::isalnum(c) || c == '.' || c == '_' || c == '-') {
            out.push_back(static_cast<char>(c));
        } else {
            out.push_back('_');
        }
    }
    // Collapse leading dots so we never emit ".." segments.
    while (!out.empty() && out.front() == '.') out.erase(out.begin());
    return out;
}

// Durable path for the *player* (glass) process — player-authoritative store
// under stream (SP2T). One file per game so a player can attach to different
// servers without sharing state.
//
// Layout: <prefPath>/games/<sanitized-gameId>.db
//   prefPath — SDL_GetPrefPath for the player shell (e.g. "squz"/"spyder-player")
//   gameId   — server catalogue name (appName), e.g. "tiltbuggy"
//
// Caller must ensure the parent directory exists before opening the file.
inline std::string durableDbPathForPlayer(const char* gameId,
                                          const char* prefPathOrNull) {
    if (!gameId || !*gameId || !prefPathOrNull || !*prefPathOrNull)
        return ":memory:";
    const std::string id = sanitizeGameId(gameId);
    if (id.empty()) return ":memory:";
    return std::string(prefPathOrNull) + "games/" + id + ".db";
}

// ── SP2T snapshot helpers (full SQLite serialize of "main") ──────────
// Wire payload after MessageHeader{kSqlpipeMsgMagic}: raw sqlite3_serialize
// bytes of the main schema. Player holds durable file; server applies into
// :memory: on attach and pushes continuously (rate-limited) + on detach so
// reconnect restores.

inline bool dumpSqliteMain(sqlite3* db, std::vector<uint8_t>& out) {
    out.clear();
    if (!db) return false;
    sqlite3_int64 sz = 0;
    unsigned char* p = sqlite3_serialize(db, "main", &sz, 0);
    if (!p || sz <= 0) {
        if (p) sqlite3_free(p);
        return false;
    }
    out.assign(p, p + static_cast<size_t>(sz));
    sqlite3_free(p);
    return true;
}

// Replace "main" on an open connection with a serialize blob.
// On success the connection owns a copy of the data.
inline bool loadSqliteMain(sqlite3* db, std::span<const uint8_t> blob) {
    if (!db || blob.empty()) return false;
    // Copy for SQLite ownership (FREEONCLOSE).
    auto* copy = static_cast<unsigned char*>(sqlite3_malloc64(blob.size()));
    if (!copy) return false;
    std::memcpy(copy, blob.data(), blob.size());
    const int rc = sqlite3_deserialize(
        db, "main", copy, static_cast<sqlite3_int64>(blob.size()),
        static_cast<sqlite3_int64>(blob.size()),
        SQLITE_DESERIALIZE_FREEONCLOSE | SQLITE_DESERIALIZE_RESIZEABLE);
    if (rc != SQLITE_OK) {
        sqlite3_free(copy);
        return false;
    }
    return true;
}

// True when the open db has at least one user table (not sqlite_* / _sqlpipe_*).
// Used to skip empty player seeds that would clobber the server's schemaDdl.
inline bool sqliteHasUserTables(sqlite3* db) {
    if (!db) return false;
    sqlite3_stmt* stmt = nullptr;
    const char* sql =
        "SELECT 1 FROM sqlite_master WHERE type='table' "
        "AND name NOT LIKE 'sqlite_%' AND name NOT LIKE '\\_sqlpipe\\_%' ESCAPE '\\' "
        "LIMIT 1";
    if (sqlite3_prepare_v2(db, sql, -1, &stmt, nullptr) != SQLITE_OK)
        return false;
    const bool has = sqlite3_step(stmt) == SQLITE_ROW;
    sqlite3_finalize(stmt);
    return has;
}

} // namespace spyder
