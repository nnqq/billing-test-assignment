.PHONY: test test-integration test-e2e lint build up down logs smoke import

test:
	go test ./...

test-integration:
	MONGO_TEST_URI=mongodb://localhost:27017 go test ./internal/storage/mongodb/ -count=1 -v

test-e2e:
	API_BASE_URL=http://localhost:8080 go test ./e2e/ -count=1 -v

# gofmt -l names the offenders but exits 0, so the check has to be the
# emptiness of its output rather than its status.
lint:
	@unformatted=$$(gofmt -l ./cmd ./internal ./e2e); \
	if [ -n "$$unformatted" ]; then \
		echo "These files need gofmt:"; \
		echo "$$unformatted"; \
		exit 1; \
	fi
	go vet ./...

build:
	go build ./...

import:
	MONGO_URI=mongodb://localhost:27017 \
	MONGO_DB=billing-test-assignment \
	CSV_PATH=testdata/transactions.csv \
	go run ./cmd/importer

up:
	docker compose up --build -d

down:
	docker compose down -v

logs:
	docker compose logs -f api

smoke:
	curl -s localhost:8080/healthz
	curl -s localhost:8080/merchants
	curl -s "localhost:8080/merchants/M-1001/summary"
