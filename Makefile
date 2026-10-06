.PHONY: build build-web test test-race run docker clean

build-web:
	cd web && npm ci && npm run build

build: build-web
	go build -trimpath -o dist/115-direct ./cmd/115-direct

test:
	go test ./...
	cd web && npm run typecheck

test-race:
	go test -race ./...

run:
	go run ./cmd/115-direct

docker:
	docker build -t 115-direct:local .

clean:
	rm -rf dist web/dist web/node_modules
