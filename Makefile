BINARY_NAME=pw-autopaused

.PHONY: build

build:
	go build -o $(BINARY_NAME) .

dev:
	go run .
