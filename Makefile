.PHONY: bullseye pre-release build test test-report test-integration vet fmt-check clean player player-web sign release-dist release-tap release

build: bin/spyder bin/ios bin/spyder-killusbmuxd

# Spyder player (stream glass). Self-contained C++ tree under player/;
# speaks the GE wire over spyder's relay — no link against the ge engine.
player:
	$(MAKE) -C player

# Browser player (🎯T101/🎯T106): same tree compiled to wasm; the daemon
# serves player/web/dist at /player/.
player-web:
	$(MAKE) -C player web

# Parent ../go.work (claudia/jevons) does not list this module; force
# module mode so vet/test/build work from a sibling checkout.
GOWORK ?= off
export GOWORK

# Studio secrets (🎯T133) need Security.framework via cgo. Default on
# darwin is CGO_ENABLED=1 when a cgo file is selected; force it so a
# parent shell with CGO_ENABLED=0 cannot silently drop SecItem*.
bin/spyder: $(shell find . -name '*.go' -not -path './bin/*' -not -path './cmd/*' 2>/dev/null) go.mod go.sum
	CGO_ENABLED=1 go build -ldflags "-X main.version=dev" -o bin/spyder .

# sign attaches a stable Development / Developer ID Application identity
# and identifier com.marcelocantos.spyder so keychain ACLs do not churn
# across rebuilds (🎯T133.1). Required before secret import / fastlane.
sign: bin/spyder
	@./scripts/codesign-spyder.sh bin/spyder

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
	CGO_ENABLED=1 go test ./...

vet:
	CGO_ENABLED=1 go vet ./...

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
	@CGO_ENABLED=1 go vet ./... && echo "✓ vet"
	@CGO_ENABLED=1 go build -ldflags "-X main.version=dev" -o bin/spyder . && echo "✓ build"
	@go build -mod=mod -o bin/ios github.com/danielpaulus/go-ios && echo "✓ build ios"
	@CGO_ENABLED=1 go test ./... 2>&1 | tail -20 && echo "✓ tests"
	@dirty=$$(git status --porcelain | grep -vE 'bullseye\.yaml$$' || true); \
	if [ -z "$$dirty" ]; then echo "✓ working tree clean"; \
	else \
	  echo ""; \
	  echo "================================================================"; \
	  echo "⚠  DIRTY WORKING TREE"; \
	  echo ""; \
	  echo "Warning only — invariants still pass (exit 0)."; \
	  echo "Look at the files below before starting a new target."; \
	  echo "Leftover work from a different objective → park it in a commit first."; \
	  echo "This session's WIP on the recommended target → continue."; \
	  echo "================================================================"; \
	  echo "$$dirty"; \
	  echo "================================================================"; \
	  echo ""; \
	fi

pre-release: bullseye

# Local release (🎯T135). VERSION is required for package:
#   VERSION=0.86.0 make release-dist
#   VERSION=0.86.0 make release
# release-tap may omit VERSION when dist/ already holds the tarball.
release-dist:
	./scripts/release-package.sh $(VERSION)

release-tap:
	./scripts/release-tap.sh $(VERSION)

release: pre-release release-dist
	./scripts/release-publish.sh $(VERSION)

clean:
	rm -rf bin/
	$(MAKE) -C player clean
