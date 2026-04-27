.PHONY: bullseye pre-release build test test-report test-integration vet fmt-check clean

build:
	go build -ldflags "-X main.version=dev" -o bin/spyder .

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

# test-integration runs the real-bridge integration tier only.
# (Also exposed by test-report as the "integration" suite when
# SPYDER_INTEGRATION=1.)
test-integration:
	@go test -tags=integration ./internal/pmd3bridge/...

bullseye:
	@test -z "$$(gofmt -l .)" && echo "✓ fmt" || \
	 (echo "✗ gofmt issues:"; gofmt -l .; exit 1)
	@go vet ./... && echo "✓ vet"
	@go build -ldflags "-X main.version=dev" -o bin/spyder . && echo "✓ build"
	@go test ./... 2>&1 | tail -20 && echo "✓ tests"
	@test -z "$$(git status --porcelain)" && echo "✓ clean" || \
	 (echo "✗ dirty tree:"; git status --short; exit 1)

# pre-release runs all dev invariants AND requires TEST-REPORT.json to be
# fresh against HEAD. Test-report freshness is a release gate, not a dev-loop
# invariant — staleness is enforced by the pre-push hook (= going to merge =
# pre-release boundary), not by `make bullseye` (= "what should I work on?").
pre-release: bullseye
	@./scripts/check-test-report-fresh.sh

clean:
	rm -rf bin/
