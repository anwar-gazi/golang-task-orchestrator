.PHONY: help build run test clean docker-build docker-up docker-down docker-logs migrate

# Default target
help:
	@echo "Task Orchestrator - Available Commands:"
	@echo ""
	@echo "  make build              - Build the Go binary"
	@echo "  make run                - Run the orchestrator locally"
	@echo "  make test               - Run tests"
	@echo "  make clean              - Clean build artifacts"
	@echo ""
	@echo "  make docker-build       - Build Docker image"
	@echo "  make docker-up          - Start single-instance mode"
	@echo "  make docker-up-dist     - Start distributed mode (3 workers)"
	@echo "  make docker-down        - Stop all services"
	@echo "  make docker-logs        - View logs"
	@echo ""
	@echo "  make migrate            - Run database migrations"
	@echo "  make psql               - Connect to PostgreSQL"
	@echo ""
	@echo "  make enqueue-example    - Enqueue an example task"
	@echo "  make monitor            - Start with Prometheus and Grafana"

# Build the Go binary
build:
	go build -o bin/orchestrator ./cmd/orchestrator

# Run locally
run: build
	./bin/orchestrator

# Run tests
test:
	go test -v ./...

# Clean build artifacts
clean:
	rm -rf bin/
	go clean

# Docker commands
docker-build:
	docker-compose build

docker-up:
	docker-compose up -d
	@echo "Orchestrator running on http://localhost:9090"
	@echo "Health: http://localhost:9090/health"
	@echo "Metrics: http://localhost:9090/metrics"

docker-up-dist:
	docker-compose -f docker-compose.distributed.yml up -d
	@echo "Distributed orchestrator running:"
	@echo "  Worker 1: http://localhost:9091"
	@echo "  Worker 2: http://localhost:9092"
	@echo "  Worker 3: http://localhost:9093"
	@echo "  Prometheus: http://localhost:9090"
	@echo "  Grafana: http://localhost:3000 (admin/admin)"

docker-down:
	docker-compose down
	docker-compose -f docker-compose.distributed.yml down

docker-logs:
	docker-compose logs -f

docker-logs-dist:
	docker-compose -f docker-compose.distributed.yml logs -f

# Start with monitoring stack
monitor:
	docker-compose --profile monitoring up -d
	@echo "Services running:"
	@echo "  Orchestrator: http://localhost:9090"
	@echo "  Prometheus: http://localhost:9091"
	@echo "  Grafana: http://localhost:3000 (admin/admin)"

# Database operations
migrate:
	@echo "Running migrations..."
	@docker-compose exec postgres psql -U postgres -d orchestrator -f /docker-entrypoint-initdb.d/001_initial_schema.sql || \
	psql ${DATABASE_URL} -f migrations/001_initial_schema.sql

psql:
	docker-compose exec postgres psql -U postgres -d orchestrator

# Task operations
enqueue-example:
	@echo "Enqueuing example task..."
	@curl -X POST http://localhost:9090/api/tasks \
		-H "Content-Type: application/json" \
		-d '{"type":"example:task","payload":{"message":"Hello!"}}'

# Development
dev:
	docker-compose up --build

dev-dist:
	docker-compose -f docker-compose.distributed.yml up --build

# View metrics
metrics:
	@echo "Opening metrics endpoint..."
	@curl http://localhost:9090/metrics

health:
	@curl -s http://localhost:9090/health | jq .
