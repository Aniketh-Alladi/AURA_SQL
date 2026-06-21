.PHONY: build clean test run-aura run-demo run-smoke

build:
	go build -o bin/aura ./cmd/aura
	go build -o bin/demo ./cmd/demo
	go build -o bin/smoke ./cmd/smoke

clean:
	rm -rf bin/ ./data ./demo_data
	go clean

test:
	go test -count=1 ./...

run-aura: build
	./bin/aura

run-demo: build
	./bin/demo

run-smoke: build
	./bin/smoke

fmt:
	gofmt -w .

vet:
	go vet ./...

all: fmt vet test build