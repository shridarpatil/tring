# Tring dev convenience. Keep targets thin — anything non-trivial belongs
# in a script or in the README, not here.

.PHONY: help up down api orchestrator build sdk sdk-watch test fmt vet clean serve-demo demo-url

DEMO_PORT ?= 9999
DEMO_URL  := http://localhost:$(DEMO_PORT)/examples/sample-crm.html

help:
	@echo "Common targets:"
	@echo "  make up            - docker compose up -d  (FS + Postgres + API + orchestrator)"
	@echo "  make down          - docker compose down"
	@echo "  make logs          - tail logs of all four services"
	@echo "  make images        - rebuild both Go images (forces fresh binary)"
	@echo "  make api           - run the API on :8085 (foreground, host) — for dev hot-iterate"
	@echo "  make orchestrator  - run the orchestrator (foreground, host) — for dev hot-iterate"
	@echo "  make build         - go build ./... (both binaries)"
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
	docker compose build api orchestrator

api:
	go run ./cmd/api

orchestrator:
	go run ./cmd/orchestrator

build:
	go build ./...

sdk:
	cd sdk/js && npm install && npm run build

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
