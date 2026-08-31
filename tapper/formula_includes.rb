service do
  run [opt_bin/"spyder", "serve"]
  keep_alive true
  log_path var/"log/spyder.log"
  error_log_path var/"log/spyder.log"
  environment_variables PATH: "/opt/homebrew/bin:/opt/homebrew/sbin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin", SPYDER_ADDR: ":3030"
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
  EOS
end
