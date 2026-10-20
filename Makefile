.PHONY: test build vet fmt check

test:
	go test ./...

build:
	go build ./cmd/...

vet:
	go vet ./...

# Every other target here runs, so a formatting slip has to fail too: gofmt is the
# one Go gate `go vet` does not cover, and a file that misses it is still valid
# Go that compiles, tests and builds clean.
fmt:
	@unformatted="$$(gofmt -l . )"; \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt -l reported:"; echo "$$unformatted"; exit 1; \
	fi

check: test vet build fmt
