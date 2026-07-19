.PHONY: bullseye pre-release build test test-report test-integration vet fmt-check clean player

build: bin/spyder bin/ios bin/spyder-killusbmuxd

# Spyder player (stream glass). Self-contained C++ tree under player/;
# speaks the GE wire over spyder's relay — no link against the ge engine.
player:
	$(MAKE) -C player

bin/spyder: $(shell find . -name '*.go' -not -path './bin/*' -not -path './cmd/*' 2>/dev/null) go.mod go.sum
	go build -ldflags "-X main.version=dev" -o bin/spyder .

# bin/spyder-killusbmuxd is a single-purpose helper for the doctor's
# --fix path. It runs `killall usbmuxd` and exits. Built as a
# separate binary so the operator can grant it NOPASSWD sudo via a
# sudoers.d entry without giving the main spyder binary any
# privilege. See cmd/spyder-killusbmuxd/main.go for sudoers setup.
bin/spyder-killusbmuxd: cmd/spyder-killusbmuxd/main.go
	go build -o bin/spyder-killusbmuxd ./cmd/spyder-killusbmuxd

# bin/ios is the bundled go-ios CLI / tunnel daemon. spyder spawns
# `ios tunnel start --userspace` as a subprocess for iOS-17+ RSD
# device discovery. The binary is built from the same go-ios module
# version pinned in go.mod (with the local `replace` during the
# upstream PR shake-out).
#
# `-mod=mod` is required because go-ios's CLI pulls in deps (docopt,
# gopacket, struc, ...) that spyder itself doesn't import — `go mod
# tidy` strips them from go.sum, but the ios build needs them. mod
# mode auto-fetches them at build time.
#
# Depends on go.mod/go.sum so bumping the go-ios pin (the `replace`
# target SHA) rebuilds the bundled binary — otherwise the file target
# is considered up-to-date and `make build` silently ships the old ios.
bin/ios: go.mod go.sum
	go build -mod=mod -o bin/ios github.com/danielpaulus/go-ios

test:
	go test ./...

vet:
	go vet ./...

fmt-check:
	@test -z "$$(gofmt -l .)" || (gofmt -l .; exit 1)

# test-report runs every tier on a clean-tree HEAD and writes
# TEST-REPORT.json. The report is committed (amended into the code commit
# it vouches for) and is the CI/hook evidence that tests were actually
# run. See 🎯T26.4.
test-report:
	@./scripts/test-report.sh

# test-integration is reserved for HIL / integration tests that
# require real devices or external services. Currently a no-op stub —
# HIL coverage runs through the SPYDER_LIVE_UDID-gated _Live tests.
test-integration:
	@echo "no integration tier configured; HIL tests run via SPYDER_LIVE_UDID-gated _Live tests"

bullseye:
	@test -z "$$(gofmt -l .)" && echo "✓ fmt" || \
	 (echo "✗ gofmt issues:"; gofmt -l .; exit 1)
	@go vet ./... && echo "✓ vet"
	@go build -ldflags "-X main.version=dev" -o bin/spyder . && echo "✓ build"
	@go build -mod=mod -o bin/ios github.com/danielpaulus/go-ios && echo "✓ build ios"
	@go test ./... 2>&1 | tail -20 && echo "✓ tests"
	@test -z "$$(git status --porcelain)" && echo "✓ clean" || \
	 (echo "✗ dirty tree:"; git status --short; exit 1)

pre-release: bullseye

clean:
	rm -rf bin/
	$(MAKE) -C player clean
