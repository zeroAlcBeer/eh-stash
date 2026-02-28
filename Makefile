# Makefile for eh-stash — EH 排行榜爬虫 + 前端

# Load local overrides if exists (not tracked by git)
# Copy Makefile.local.example to Makefile.local and fill in your values
-include Makefile.local

# --- Variables ---
# Private registry on NAS. Override: make push REGISTRY_URL=x.x.x.x:5000
REGISTRY_URL ?= 192.168.0.110:5000

# docker-compose service names
API_SERVICE     := api
SCRAPER_SERVICE := scraper
FRONTEND_SERVICE := frontend
# postgres uses official image — NAS pulls it directly, no build/push needed

# Registry image names
API_IMAGE      := eh-stash-api
SCRAPER_IMAGE  := eh-stash-scraper
FRONTEND_IMAGE := eh-stash-frontend

# Tag
TAG ?= latest

# Project name (= directory name, used by docker-compose to prefix local image names)
PROJECT_NAME := $(shell basename $(CURDIR))

# Local image names produced by docker-compose build
LOCAL_API_IMAGE      := $(PROJECT_NAME)-$(API_SERVICE)
LOCAL_SCRAPER_IMAGE  := $(PROJECT_NAME)-$(SCRAPER_SERVICE)
LOCAL_FRONTEND_IMAGE := $(PROJECT_NAME)-$(FRONTEND_SERVICE)


# --- Local Development ---
.PHONY: build up down restart logs logs-api logs-scraper logs-frontend dev-rebuild

# Build all custom images (api, scraper, frontend)
build:
	@echo "--> Building Docker images..."
	docker-compose build

# Start all services in detached mode
up:
	@echo "--> Starting all services..."
	docker-compose up -d

# Stop and remove all services (data volume preserved)
down:
	@echo "--> Stopping all services..."
	docker-compose down

# Restart all services
restart: down up

# Rebuild and restart changed services without full down
dev-rebuild:
	@echo "--> Rebuilding and restarting scraper, api, frontend..."
	docker-compose stop scraper api frontend
	docker-compose rm -f scraper api frontend
	docker-compose up -d --build scraper api frontend

# Tail logs from all services
logs:
	docker-compose logs -f

logs-api:
	docker-compose logs -f api

logs-scraper:
	docker-compose logs -f scraper

logs-frontend:
	docker-compose logs -f frontend


# --- Deployment to NAS Registry ---
.PHONY: tag push release

# Tag freshly built images for the private registry
tag: build
	@echo "--> Tagging images for registry at $(REGISTRY_URL)..."
	docker tag $(LOCAL_API_IMAGE):latest      $(REGISTRY_URL)/$(API_IMAGE):$(TAG)
	docker tag $(LOCAL_SCRAPER_IMAGE):latest  $(REGISTRY_URL)/$(SCRAPER_IMAGE):$(TAG)
	docker tag $(LOCAL_FRONTEND_IMAGE):latest $(REGISTRY_URL)/$(FRONTEND_IMAGE):$(TAG)

# Push tagged images to the private registry
push:
	@echo "--> Pushing images to registry at $(REGISTRY_URL)..."
	docker push $(REGISTRY_URL)/$(API_IMAGE):$(TAG)
	docker push $(REGISTRY_URL)/$(SCRAPER_IMAGE):$(TAG)
	docker push $(REGISTRY_URL)/$(FRONTEND_IMAGE):$(TAG)

# Full release: build → tag → push
release: tag push
	@echo "--> Release complete: api, scraper, frontend pushed to $(REGISTRY_URL)."


# --- Portainer Remote Deployment ---
.PHONY: portainer-redeploy portainer-redeploy-service

PORTAINER_URL         ?= http://192.168.0.110:9000
PORTAINER_API_KEY     ?=
PORTAINER_STACK_NAME  ?=
PORTAINER_ENDPOINT_ID ?= 2

# Redeploy entire stack in Portainer (re-pull images and redeploy)
# Usage: make portainer-redeploy PORTAINER_API_KEY=xxx PORTAINER_STACK_NAME=eh-stash
portainer-redeploy:
	@echo "--> Redeploying stack '$(PORTAINER_STACK_NAME)' in Portainer..."
	@if [ -z "$(PORTAINER_API_KEY)" ]; then echo "Error: PORTAINER_API_KEY required."; exit 1; fi
	@if [ -z "$(PORTAINER_STACK_NAME)" ]; then echo "Error: PORTAINER_STACK_NAME required."; exit 1; fi
	@STACK_ID=$$(curl -s -X GET \
		"$(PORTAINER_URL)/api/stacks" \
		-H "X-API-Key: $(PORTAINER_API_KEY)" \
		| jq -r '.[] | select(.Name=="$(PORTAINER_STACK_NAME)") | .Id'); \
	if [ -z "$$STACK_ID" ] || [ "$$STACK_ID" = "null" ]; then \
		echo "Error: Stack '$(PORTAINER_STACK_NAME)' not found."; exit 1; \
	fi; \
	echo "Found stack ID: $$STACK_ID"; \
	STACK_FILE=$$(curl -s -X GET \
		"$(PORTAINER_URL)/api/stacks/$$STACK_ID/file" \
		-H "X-API-Key: $(PORTAINER_API_KEY)" \
		| jq -r '.StackFileContent'); \
	curl -s -X PUT \
		"$(PORTAINER_URL)/api/stacks/$$STACK_ID?endpointId=$(PORTAINER_ENDPOINT_ID)" \
		-H "X-API-Key: $(PORTAINER_API_KEY)" \
		-H "Content-Type: application/json" \
		-d "$$(jq -n --arg content "$$STACK_FILE" '{stackFileContent: $$content, prune: true, pullImage: true}')" \
		| jq .; \
	echo "--> Stack redeployed."

# Remove and recreate a specific service/container in Portainer stack
# Usage: make portainer-redeploy-service PORTAINER_API_KEY=your_api_key PORTAINER_STACK_NAME=your_stack SERVICE=frontend
portainer-redeploy-service:
	@echo "--> Redeploying service '$(SERVICE)' in stack '$(PORTAINER_STACK_NAME)'..."
	@if [ -z "$(PORTAINER_API_KEY)" ]; then echo "Error: PORTAINER_API_KEY required."; exit 1; fi
	@if [ -z "$(PORTAINER_STACK_NAME)" ]; then echo "Error: PORTAINER_STACK_NAME required."; exit 1; fi
	@if [ -z "$(SERVICE)" ]; then echo "Error: SERVICE required. e.g. make portainer-redeploy-service SERVICE=scraper"; exit 1; fi
	@CONTAINER_NAME="$(PORTAINER_STACK_NAME)-$(SERVICE)-1"; \
	echo "Looking for container: $$CONTAINER_NAME"; \
	CONTAINER_ID=$$(curl -s -X GET \
		"$(PORTAINER_URL)/api/endpoints/$(PORTAINER_ENDPOINT_ID)/docker/containers/json?all=true" \
		-H "X-API-Key: $(PORTAINER_API_KEY)" \
		| jq -r ".[] | select(.Names[] | contains(\"$$CONTAINER_NAME\")) | .Id"); \
	if [ -n "$$CONTAINER_ID" ] && [ "$$CONTAINER_ID" != "null" ]; then \
		echo "Stopping and removing container $$CONTAINER_ID..."; \
		curl -s -X DELETE \
			"$(PORTAINER_URL)/api/endpoints/$(PORTAINER_ENDPOINT_ID)/docker/containers/$$CONTAINER_ID?force=true" \
			-H "X-API-Key: $(PORTAINER_API_KEY)"; \
	else \
		echo "Container not found, proceeding with stack redeploy..."; \
	fi
	@$(MAKE) portainer-redeploy PORTAINER_API_KEY=$(PORTAINER_API_KEY) PORTAINER_STACK_NAME=$(PORTAINER_STACK_NAME)


# --- Help ---
.PHONY: help

help:
	@echo "Usage: make [target]"
	@echo ""
	@echo "Local Development:"
	@echo "  build                Build Docker images (api, scraper, frontend)"
	@echo "  up                   Start all services in detached mode"
	@echo "  down                 Stop and remove containers (data preserved)"
	@echo "  restart              down + up"
	@echo "  dev-rebuild          Rebuild and restart scraper/api/frontend only"
	@echo "  logs                 Tail all logs"
	@echo "  logs-api             Tail api logs"
	@echo "  logs-scraper         Tail scraper logs"
	@echo "  logs-frontend        Tail frontend logs"
	@echo ""
	@echo "Deployment:"
	@echo "  tag                  Build and tag images for NAS registry"
	@echo "  push                 Push tagged images to NAS registry"
	@echo "  release              build + tag + push (full cycle)"
	@echo ""
	@echo "Portainer (NAS):"
	@echo "  portainer-redeploy          Redeploy entire stack"
	@echo "    PORTAINER_STACK_NAME=xxx  PORTAINER_API_KEY=xxx"
	@echo "  portainer-redeploy-service  Restart one service and redeploy"
	@echo "    PORTAINER_STACK_NAME=xxx  PORTAINER_API_KEY=xxx  SERVICE=frontend"
	@echo ""
	@echo "Variables (override on command line or in Makefile.local):"
	@echo "  REGISTRY_URL          (default: 192.168.0.110:5000)"
	@echo "  TAG                   (default: latest)"
	@echo "  PORTAINER_URL         (default: http://192.168.0.110:9000)"
	@echo "  PORTAINER_ENDPOINT_ID (default: 2)"
