.PHONY: build test test-integration cover lint fmt up down

build:
	go build ./...

test:
	go test -race ./...

test-integration:
	TEST_DATABASE_URL=$${TEST_DATABASE_URL:-postgres://spin:spin@localhost:5432/transactions?sslmode=disable} \
		go test -race -tags integration ./internal/postgres/...

cover:
	go test -race -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out

lint:
	golangci-lint run

fmt:
	gofmt -w .

up:
	docker compose up --build

down:
	docker compose down -v
