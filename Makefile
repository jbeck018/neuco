.PHONY: run-api run-worker migrate-up migrate-down gen test build dev

run-api:
	air -c .air.api.toml

run-worker:
	air -c .air.worker.toml

run-api-no-air:
	go run ./cmd/server

run-worker-no-air:
	go run ./cmd/worker

migrate-up:
	migrate -path migrations -database "$$DATABASE_URL" up

migrate-down:
	migrate -path migrations -database "$$DATABASE_URL" down 1

gen:
	cd neuco-web && pnpm generate:api

test:
	go test ./... -race -count=1

build:
	go build -o bin/neuco-api ./cmd/server
	go build -o bin/neuco-worker ./cmd/worker

dev:
	docker compose up -d postgres
	@echo "Postgres running on :5432"
	@echo "Run 'make run-api' and 'make run-worker' in separate terminals"
	@echo "Run 'cd neuco-web && npm run dev' for frontend"

docker-build-api:
	docker build --target server -t neuco-api .

docker-build-worker:
	docker build --target worker -t neuco-worker .

lint:
	golangci-lint run ./...

clean:
	rm -rf bin/
	docker compose down -v
