BINARY_NAME=pw-autopaused

.PHONY: build

build:
	CGO_ENABLED=1 go build -o $(BINARY_NAME) .

dev:
	go run .
