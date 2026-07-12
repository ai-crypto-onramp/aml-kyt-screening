.PHONY: build test run lint docker-build docker-run clean migrate-up migrate-down

build:
	go build -o bin/server ./cmd/kyt

test:
	go test ./... -race -coverprofile=coverage.out -coverpkg=./...

run:
	go run ./cmd/kyt

lint:
	go vet ./...

docker-build:
	docker build -t ai-crypto-onramp/aml-kyt-screening .

docker-run:
	docker run --rm -p 8080:8080 ai-crypto-onramp/aml-kyt-screening

clean:
	rm -rf bin/ coverage.out
