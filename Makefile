.PHONY: build test run lint cover docker-build docker-run clean migrate-up migrate-down

build:
	go build -o bin/aml-kyt-screening ./cmd/kyt

test:
	go test ./internal/... -race -coverprofile=coverage.out -coverpkg=./internal/...

run:
	go run ./cmd/kyt

lint:
	golangci-lint run

cover: test
	go tool cover -func=coverage.out | tail -1

docker-build:
	docker build -t ai-crypto-onramp/aml-kyt-screening .

docker-run:
	docker run --rm -p 8080:8080 ai-crypto-onramp/aml-kyt-screening

migrate-up:
	go run ./cmd/migrate --up

migrate-down:
	go run ./cmd/migrate --down

clean:
	rm -rf bin/ coverage.out
