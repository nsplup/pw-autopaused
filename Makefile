BINARY_NAME=pw-autopaused

.PHONY: build

build:
	go build -o $(BINARY_NAME) main.go

dev:
	go run main.go
