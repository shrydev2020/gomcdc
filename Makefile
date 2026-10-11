.DEFAULT_GOAL := help

SELF_MCDC_REPORT ?= coverage-self-mcdc.json

.PHONY: help test race vet coverage coverage-html self-mcdc self-mcdc-html

help: ## Show available targets and usage.
	@printf 'Usage: make <target>\n\nTargets:\n'; \
	awk 'BEGIN { FS = ":.*##" } /^[[:alnum:]_.-]+:.*##/ { printf "  %-18s %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

test: ## Run all Go tests.
	go test -count=1 ./...

race: ## Run all Go tests with the race detector.
	go test -count=1 -race ./...

vet: ## Run go vet on all packages.
	go vet ./...

coverage: ## Show standard Go statement coverage by package.
	go test -count=1 -cover ./...

coverage-html: ## Create the standard Go coverage report in coverage.html.
	go test -count=1 -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o=coverage.html

self-mcdc: ## Generate gomcdc's self-MC/DC report (override with SELF_MCDC_REPORT=path).
	go run . test \
		--timeout=10m \
		--coverage=decision,condition,mcdc-unique,mcdc-masking \
		--strict \
		--format=json \
		--output="$(SELF_MCDC_REPORT)" \
		./...

self-mcdc-html: ## Create gomcdc's self-MC/DC report in coverage-html/.
	go run . test \
		--timeout=10m \
		--coverage=decision,condition,mcdc-unique,mcdc-masking \
		--strict \
		--format=html \
		--output=coverage-html \
		./...
