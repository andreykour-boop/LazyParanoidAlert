# Builds the siren agent for the target platforms into bin/:
# windows/amd64, linux/amd64, linux/arm64.
# Usage: make build [VERSION=0.1.0]
VERSION ?= 0.1.0-dev

.PHONY: build test vet clean

build:
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o bin/siren-windows-amd64.exe ./cmd/siren
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o bin/siren-linux-amd64 ./cmd/siren
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o bin/siren-linux-arm64 ./cmd/siren

test:
	go test ./...

vet:
	go vet ./...

clean:
	rm -rf bin
