.PHONY: test build vet check

test:
	go test ./...

build:
	go build ./cmd/duo

vet:
	go vet ./...

check: test vet build
