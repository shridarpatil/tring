# Tring dev convenience. Keep targets thin — anything non-trivial belongs
# in a script or in the README, not here.

.PHONY: help up down api orchestrator build sdk sdk-watch test fmt vet clean serve-demo demo-url

DEMO_PORT ?= 9999
DEMO_URL  := http://localhost:$(DEMO_PORT)/examples/sample-crm.html

help:
	@echo "Common targets:"
	@echo "  make up            - docker compose up -d  (FS + Postgres + API + orchestrator + gateway)"
	@echo "  make down          - docker compose down"
	@echo "  make logs          - tail logs of all services"
	@echo "  make images        - rebuild the Go images (forces fresh binary)"
	@echo "  make api           - run the API on :8085 (foreground, host) — for dev hot-iterate"
	@echo "  make orchestrator  - run the orchestrator (foreground, host) — for dev hot-iterate"
	@echo "  make gateway       - run the WebRTC media gateway on :8087 (foreground, host)"
	@echo "  make build         - go build ./... (all binaries)"
	@echo "  make sdk           - npm run build in sdk/js"
	@echo "  make sdk-watch     - rebuild SDK on change"
	@echo "  make test          - go test ./..."
	@echo "  make vet           - go vet ./..."
	@echo "  make serve-demo    - serve repo over :$(DEMO_PORT) and open the demo CRM"
	@echo "  make demo-url      - print the demo URL"

up:
	docker compose up -d

down:
	docker compose down

logs:
	docker compose logs -f --tail=20

images:
	docker compose build api orchestrator gateway

api:
	go run ./cmd/api

orchestrator:
	go run ./cmd/orchestrator

gateway:
	go run ./cmd/gateway

build:
	go build ./...

sdk:
	cd sdk/js && npm install && npm run build
	@# Copy the drop-in bundles into the API's embed dir under their served
	@# names so /sdk/voice.js etc. are hosted by the binary (no CDN needed).
	mkdir -p internal/widget/dist
	cp sdk/js/dist/voice.iife.js internal/widget/dist/voice.js
	cp sdk/js/dist/agent.iife.js internal/widget/dist/agent.js
	cp sdk/js/dist/tring.iife.js internal/widget/dist/tring.js

sdk-watch:
	cd sdk/js && npm run watch

test:
	go test ./...

vet:
	go vet ./...

serve-demo:
	@echo ""
	@echo "  Tring demo CRM:  $(DEMO_URL)"
	@echo ""
	@( sleep 1 && (xdg-open $(DEMO_URL) || open $(DEMO_URL) || true) >/dev/null 2>&1 ) &
	@python3 -m http.server $(DEMO_PORT)

demo-url:
	@echo $(DEMO_URL)

clean:
	rm -rf sdk/js/dist sdk/js/node_modules
	go clean -cache
