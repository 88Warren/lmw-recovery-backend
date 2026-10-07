BINARY    := lmw-server
BUILD_DIR := ./bin
CMD       := ./cmd/server

.PHONY: dev run build tidy docker-build docker-run clean

## Live-reload development server (loads .env.development)
dev:
	APP_ENV=development air

## Run without live-reload, development env
run:
	APP_ENV=development go run $(CMD)/main.go

## Build a production binary (loads .env.production at runtime)
build:
	CGO_ENABLED=0 go build -ldflags="-s -w" -o $(BUILD_DIR)/$(BINARY) $(CMD)/main.go

## Tidy go modules
tidy:
	go mod tidy

## Build the Docker image
docker-build:
	docker build -t lmw-recovery-api:latest .

## Run the Docker container (set APP_ENV=production via --env-file or -e)
docker-run:
	docker run --env-file .env.production -e APP_ENV=production -p 8080:8080 lmw-recovery-api:latest

## Remove built binaries
clean:
	rm -rf $(BUILD_DIR)
