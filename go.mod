module github.com/marcelocantos/spyder

go 1.26.1

require (
	github.com/danielpaulus/go-ios v1.0.213
	github.com/google/uuid v1.6.0
	github.com/mark3labs/mcp-go v0.47.0
	gopkg.in/yaml.v3 v3.0.1
	modernc.org/sqlite v1.50.0
)

require (
	github.com/Masterminds/semver v1.5.0 // indirect
	github.com/cenkalti/backoff v2.2.1+incompatible // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/go-task/slim-sprig v0.0.0-20230315185526-52ccab3ef572 // indirect
	github.com/google/btree v1.1.2 // indirect
	github.com/google/jsonschema-go v0.4.2 // indirect
	github.com/google/pprof v0.0.0-20250317173921-a4b03ec1a45e // indirect
	github.com/grandcat/zeroconf v1.0.0 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/miekg/dns v1.1.57 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/onsi/ginkgo/v2 v2.9.5 // indirect
	github.com/pierrec/lz4 v2.6.1+incompatible // indirect
	github.com/quic-go/qtls-go1-20 v0.4.1 // indirect
	github.com/quic-go/quic-go v0.40.1-0.20231203135336-87ef8ec48d55 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/sirupsen/logrus v1.9.3 // indirect
	github.com/songgao/water v0.0.0-20200317203138-2b4b6d7c09d8 // indirect
	github.com/spf13/cast v1.7.1 // indirect
	github.com/tadglines/go-pkgs v0.0.0-20210623144937-b983b20f54f9 // indirect
	github.com/vishvananda/netlink v1.3.1 // indirect
	github.com/vishvananda/netns v0.0.5 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	go.mozilla.org/pkcs7 v0.0.0-20210826202110-33d05740a352 // indirect
	go.uber.org/mock v0.3.0 // indirect
	golang.org/x/crypto v0.48.0 // indirect
	golang.org/x/exp v0.0.0-20230725093048-515e97ebf090 // indirect
	golang.org/x/mod v0.33.0 // indirect
	golang.org/x/net v0.50.0 // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/sys v0.42.0 // indirect
	golang.org/x/text v0.34.0 // indirect
	golang.org/x/time v0.5.0 // indirect
	golang.org/x/tools v0.42.0 // indirect
	golang.zx2c4.com/wintun v0.0.0-20230126152724-0fa3db229ce2 // indirect
	gvisor.dev/gvisor v0.0.0-20240405191320-0878b34101b5 // indirect
	howett.net/plist v0.0.0-20200419221736-3b63eb3a43b5 // indirect
	modernc.org/libc v1.72.0 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
	software.sslmate.com/src/go-pkcs12 v0.2.0 // indirect
)

// Patched go-ios fork. Track the `spyder-patches` branch in
// marcelocantos/go-ios — it's a long-lived branch that rebases onto
// upstream/main periodically, carrying only the patches spyder needs
// that haven't (or shouldn't) merge upstream. Pin to the branch's
// current tip SHA via the pseudo-version. When pulling in a new
// upstream:
//   1. cd into the fork, `git fetch upstream`,
//      `git rebase upstream/main spyder-patches`,
//      `git push --force-with-lease origin spyder-patches`.
//   2. `go get github.com/danielpaulus/go-ios@<new-spyder-patches-sha>`
//      then bump this line.
replace github.com/danielpaulus/go-ios => github.com/marcelocantos/go-ios v1.0.214-0.20260520093604-0c634f48768f
