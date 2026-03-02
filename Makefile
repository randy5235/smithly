.PHONY: build vet test lint clean check

build:
	go build ./...

vet:
	go vet ./...

test:
	go test ./...

lint:
	golangci-lint run ./...

check: build vet test lint

clean:
	go clean ./...
