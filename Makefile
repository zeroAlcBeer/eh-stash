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
PI_SYNC_SERVICE := pi-sync
# postgres uses official image — NAS pulls it directly, no build/push needed
# pi-sync is built standalone (not in docker-compose.yaml — Pi-only service)

# Registry image names
API_IMAGE      := eh-stash-api
SCRAPER_IMAGE  := eh-stash-scraper
FRONTEND_IMAGE := eh-stash-frontend
PI_SYNC_IMAGE  := eh-stash-pi-sync

# Tag
TAG ?= latest

# Immutable tag derived from git HEAD (use via `make release-sha`)
GIT_SHA := $(shell git rev-parse --short=12 HEAD 2>/dev/null)

# Project name (= directory name, used by docker-compose to prefix local image names)
PROJECT_NAME := $(shell basename $(CURDIR))

# Local image names produced by docker-compose build
LOCAL_API_IMAGE      := $(PROJECT_NAME)-$(API_SERVICE)
LOCAL_SCRAPER_IMAGE  := $(PROJECT_NAME)-$(SCRAPER_SERVICE)
LOCAL_FRONTEND_IMAGE := $(PROJECT_NAME)-$(FRONTEND_SERVICE)
LOCAL_PI_SYNC_IMAGE  := $(PROJECT_NAME)-$(PI_SYNC_SERVICE)


# --- Local Development ---
.PHONY: build up down restart logs logs-api logs-scraper logs-frontend dev-rebuild

# Build all custom images (api, scraper, frontend, pi-sync)
build:
	@echo "--> Building Docker images..."
	docker-compose build
	@echo "--> Building pi-sync image (standalone)..."
	docker build -t $(LOCAL_PI_SYNC_IMAGE):latest ./pi-sync

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
.PHONY: tag push release-sha image-sha verify-remote diagnose

# Tag freshly built images for the private registry (uses TAG variable, defaults to :latest)
tag: build
	@echo "--> Tagging images for registry at $(REGISTRY_URL)..."
	docker tag $(LOCAL_API_IMAGE):latest      $(REGISTRY_URL)/$(API_IMAGE):$(TAG)
	docker tag $(LOCAL_SCRAPER_IMAGE):latest  $(REGISTRY_URL)/$(SCRAPER_IMAGE):$(TAG)
	docker tag $(LOCAL_FRONTEND_IMAGE):latest $(REGISTRY_URL)/$(FRONTEND_IMAGE):$(TAG)
	docker tag $(LOCAL_PI_SYNC_IMAGE):latest  $(REGISTRY_URL)/$(PI_SYNC_IMAGE):$(TAG)

# Push tagged images to the private registry (uses TAG variable)
push:
	@echo "--> Pushing images to registry at $(REGISTRY_URL)..."
	docker push $(REGISTRY_URL)/$(API_IMAGE):$(TAG)
	docker push $(REGISTRY_URL)/$(SCRAPER_IMAGE):$(TAG)
	docker push $(REGISTRY_URL)/$(FRONTEND_IMAGE):$(TAG)
	docker push $(REGISTRY_URL)/$(PI_SYNC_IMAGE):$(TAG)

# Recommended release: build all 4 images + tag with :latest AND :$(GIT_SHA),
# push both. Use the resulting SHA in the pi's /opt/stacks/ehstash/.env (TAG=...).
release-sha: build
	@if [ -z "$(GIT_SHA)" ]; then echo "Error: not in a git repo"; exit 1; fi
	@if ! git diff-index --quiet HEAD --; then \
		echo "Warning: working tree dirty — :$(GIT_SHA) will NOT represent committed state"; \
	fi
	@echo "--> Tagging + pushing 4 images @ $(GIT_SHA) ..."
	@for entry in \
		"$(LOCAL_API_IMAGE):$(API_IMAGE)" \
		"$(LOCAL_SCRAPER_IMAGE):$(SCRAPER_IMAGE)" \
		"$(LOCAL_FRONTEND_IMAGE):$(FRONTEND_IMAGE)" \
		"$(LOCAL_PI_SYNC_IMAGE):$(PI_SYNC_IMAGE)" \
	; do \
		LOCAL=$${entry%%:*}; \
		REMOTE=$${entry#*:}; \
		echo "  [$$REMOTE]"; \
		docker tag  $$LOCAL:latest $(REGISTRY_URL)/$$REMOTE:latest          || exit 1; \
		docker tag  $$LOCAL:latest $(REGISTRY_URL)/$$REMOTE:$(GIT_SHA)       || exit 1; \
		docker push $(REGISTRY_URL)/$$REMOTE:latest                          || exit 1; \
		docker push $(REGISTRY_URL)/$$REMOTE:$(GIT_SHA)                      || exit 1; \
	done
	@echo ""
	@echo "===================================================="
	@echo "  Released eh-stash @ $(GIT_SHA)"
	@echo "  4 images: api, scraper, frontend, pi-sync"
	@echo "  Each tagged :latest AND :$(GIT_SHA)"
	@echo ""
	@echo "  Next on pi:"
	@echo "    edit /opt/stacks/ehstash/.env  →  TAG=$(GIT_SHA)"
	@echo "    cd /opt/stacks/ehstash && docker compose pull && docker compose up -d"
	@echo "===================================================="

# Print local repo digests (manifest SHAs from registry) for all 4 images.
# Useful to verify what `make release-sha` will push, or what's currently pushed.
image-sha:
	@for img in $(API_IMAGE) $(SCRAPER_IMAGE) $(FRONTEND_IMAGE) $(PI_SYNC_IMAGE); do \
		DIGEST=$$(docker inspect $(REGISTRY_URL)/$$img:$(TAG) --format '{{range .RepoDigests}}{{println .}}{{end}}' 2>/dev/null \
			| grep "^$(REGISTRY_URL)/$$img@" | head -1); \
		if [ -n "$$DIGEST" ]; then \
			echo "  $$DIGEST"; \
		else \
			echo "  $(REGISTRY_URL)/$$img:$(TAG): <not pushed yet>"; \
		fi; \
	done

# Query the private registry directly for the current :$(TAG) manifest digest of each image.
# Compare with `image-sha` to detect stale local builds.
verify-remote:
	@for img in $(API_IMAGE) $(SCRAPER_IMAGE) $(FRONTEND_IMAGE) $(PI_SYNC_IMAGE); do \
		DIGEST=$$(curl -sSI -H "Accept: application/vnd.docker.distribution.manifest.v2+json" \
			http://$(REGISTRY_URL)/v2/$$img/manifests/$(TAG) \
			| awk 'tolower($$1) == "docker-content-digest:" { print $$2 }' | tr -d '\r'); \
		if [ -z "$$DIGEST" ]; then \
			echo "  $(REGISTRY_URL)/$$img:$(TAG): <not found in registry>"; \
		else \
			echo "  $(REGISTRY_URL)/$$img@$$DIGEST"; \
		fi; \
	done

# Show local + remote side by side
diagnose:
	@echo "--- Local (last build/push) ---"
	@$(MAKE) image-sha --no-print-directory
	@echo "--- Remote (current registry manifest for :$(TAG)) ---"
	@$(MAKE) verify-remote --no-print-directory


# --- Help ---
.PHONY: help

help:
	@echo "Usage: make [target]"
	@echo ""
	@echo "Local Development:"
	@echo "  build                Build Docker images (api, scraper, frontend, pi-sync)"
	@echo "  up                   Start all services in detached mode"
	@echo "  down                 Stop and remove containers (data preserved)"
	@echo "  restart              down + up"
	@echo "  dev-rebuild          Rebuild and restart scraper/api/frontend only"
	@echo "  logs                 Tail all logs"
	@echo "  logs-api             Tail api logs"
	@echo "  logs-scraper         Tail scraper logs"
	@echo "  logs-frontend        Tail frontend logs"
	@echo ""
	@echo "Deployment (Pi private registry $(REGISTRY_URL)):"
	@echo "  release-sha          build + tag :latest AND :$(GIT_SHA) + push both"
	@echo "  tag                  build + tag for :$(TAG) (low-level, no push)"
	@echo "  push                 push :$(TAG) (low-level, no build)"
	@echo "  image-sha            print local repo digests for all 4 images"
	@echo "  verify-remote        query registry for current :$(TAG) manifest digests"
	@echo "  diagnose             local + remote side by side"
	@echo ""
	@echo "Pi deployment flow (after make release-sha):"
	@echo "  on pi:  edit /opt/stacks/ehstash/.env  →  TAG=<sha>"
	@echo "          cd /opt/stacks/ehstash && docker compose pull && docker compose up -d"
	@echo ""
	@echo "Variables (override on command line or in Makefile.local):"
	@echo "  REGISTRY_URL          (default: 192.168.0.110:5000)"
	@echo "  TAG                   (default: latest)"
