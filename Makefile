# Makefile for bend2will

.PHONY: all build test vet clean cross-compile

all: build

build:
	go build ./...

test:
	go test ./... -count=1 -timeout 120s

vet:
	go vet ./...

clean:
	rm -rf .tmp/bin/*

cross-compile:
	GOOS=windows GOARCH=amd64 go build -o .tmp/bin/obey.exe ./cmd/obey
	GOOS=linux GOARCH=amd64 go build -o .tmp/bin/obey-linux ./cmd/obey
	GOOS=darwin GOARCH=amd64 go build -o .tmp/bin/obey-darwin ./cmd/obey
