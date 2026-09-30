.PHONY: test build vet check

test:
	go test ./...

build:
	go build ./cmd/...

vet:
	go vet ./...

check: test vet build
