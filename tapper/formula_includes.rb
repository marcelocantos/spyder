service do
  run [opt_bin/"spyder", "serve"]
  keep_alive true
  log_path var/"log/spyder.log"
  error_log_path var/"log/spyder.log"
  # adb (Homebrew prefix), xcrun and ioreg (system dirs) — all on std_service_path_env.
  environment_variables PATH: std_service_path_env, SPYDER_ADDR: ":3030"
end

def caveats
  <<~EOS
    spyder runs as a persistent HTTP MCP server on :3030.
    It is NOT started automatically on install — start it with:

      brew services start spyder

    Then register it with your agent (Claude Code shown):

      claude mcp add --scope user --transport http spyder http://localhost:3030/mcp

    Verify:

      brew services list | grep spyder    # should be "started"
      lsof -iTCP:3030 -sTCP:LISTEN        # should show spyder

    If MCP tools disappear mid-session, the daemon likely stopped
    — restart with `brew services restart spyder`.

    spyder spawns a bundled `ios` binary (go-ios) as a child
    process for iOS-17+ device discovery. No system LaunchDaemon
    or sudo is required — the tunnel runs in user-space mode.

    launch_player's stream-player artifacts (🎯T102) ship under
    #{opt_libexec}/spyder/player/:

      desktop/player        — always included
      android/player.apk    — included when the release build produced
                               one (player/android's native build is not
                               yet ported to sokol_gfx; see 🎯T34)

    iOS is NOT shipped here: a Development/Ad Hoc signed Player.app
    from one team fails ApplicationVerificationFailed on any other
    developer's device — codesign/provisioning is per-developer and
    not multi-tenant. Build player/ios locally with Xcode/xcodegen
    (see player/Makefile and player/ios/CMakeLists.txt) and codesign
    it yourself, or point SPYDER_PLAYER_IOS at a re-signed .app.
    launch_player's resolution order is always:

      path= override → SPYDER_PLAYER_* env → this libexec layout →
      repo-relative dev build outputs
  EOS
end
