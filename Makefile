.PHONY: bullseye build test vet fmt-check clean

build:
	go build -ldflags "-X main.version=dev" -o bin/spyder .

test:
	go test ./...

vet:
	go vet ./...

fmt-check:
	@test -z "$$(gofmt -l .)" || (gofmt -l .; exit 1)

bullseye:
	@test -z "$$(gofmt -l .)" && echo "✓ fmt" || \
	 (echo "✗ gofmt issues:"; gofmt -l .; exit 1)
	@go vet ./... && echo "✓ vet"
	@go build -ldflags "-X main.version=dev" -o bin/spyder . && echo "✓ build"
	@go test ./... 2>&1 | tail -20 && echo "✓ tests"
	@test -z "$$(git status --porcelain)" && echo "✓ clean" || \
	 (echo "✗ dirty tree:"; git status --short; exit 1)

clean:
	rm -rf bin/
