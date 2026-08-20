GOPATH_FWD := $(shell go env GOPATH)
GOLANGCI := $(GOPATH_FWD)/bin/golangci-lint run

.PHONY: install fmt lint test test-e2e rls-mutation migrate migrate-new seed seed-staff job jobs dev up down nuke env

install:
	go install github.com/air-verse/air@latest
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
	go install golang.org/x/tools/cmd/goimports@latest
	go install github.com/golangci/golines@latest
	go install github.com/jackc/tern/v2@latest

fmt:
	$(GOPATH_FWD)/bin/goimports -w .
	$(GOPATH_FWD)/bin/golines -m 80 -w .

lint:
	go vet ./...
	$(GOLANGCI)

test:
	go test -count=1 -race ./...

test-e2e:
	cd e2e && GOWORK=off go test -count=1 ./...

migrate:
	go run ./cmd/migrate

migrate-new:
	$(GOPATH_FWD)/bin/tern new -m migrations $(name)

seed:
	docker compose exec -T postgres \
		psql -U postgres -d bokarn -v ON_ERROR_STOP=1 < seed/seed.sql
	docker compose exec -T postgres \
		psql -U postgres -d bokarn -v ON_ERROR_STOP=1 < seed/seed_pricing.sql

seed-staff:
	./scripts/seed-staff.sh

# Proves the isolation suite is not vacuous by removing each protection in turn
# and asserting the suite notices.
rls-mutation:
	./scripts/rls-mutation.sh

# Every background job the API runs on a ticker is reachable here by name, so
# a smoke test can trigger one without waiting for its cadence.
job:
	go run ./cmd/job run $(NAME)

jobs:
	go run ./cmd/job list

# compose declares env_file, so a clone with no .env fails before any service
# starts. The template holds only local development values.
env:
	@test -f .env || (cp .env.example .env && echo "created backend/.env from .env.example")

up: env
	docker compose up -d --wait postgres redis kratos kratos-staff lgtm mailpit

down:
	docker compose down

nuke:
	docker compose down -v

dev: up migrate seed seed-staff
	$(GOPATH_FWD)/bin/air
