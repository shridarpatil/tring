# Tring dev convenience. Keep targets thin — anything non-trivial belongs
# in a script or in the README, not here.

.PHONY: help up down api gateway build sdk sdk-watch test fmt vet clean demo

help:
	@echo "Common targets:"
	@echo "  make up            - docker compose up -d  (FS + Postgres + Redis + API + gateway)"
	@echo "  make down          - docker compose down"
	@echo "  make logs          - tail logs of all services"
	@echo "  make images        - rebuild the Go images (forces fresh binary)"
	@echo "  make api           - run the API on :8085 (foreground, host) — for dev hot-iterate"
	@echo "  make gateway       - run the call-plane gateway on :8087 (foreground, host)"
	@echo "  make build         - go build ./... (all binaries)"
	@echo "  make sdk           - npm run build in sdk/js"
	@echo "  make sdk-watch     - rebuild SDK on change"
	@echo "  make test          - go test ./..."
	@echo "  make vet           - go vet ./..."
	@echo "  make demo          - run the web-calling demo app (Flask, :5000) — set TRING_API_KEY first"

up: .env
	docker compose up -d

# Seed .env from the template on first run so `make up` works out of the box,
# with fresh random secrets (the binaries refuse to boot on the placeholders).
.env:
	@test -f .env || { \
		rand() { od -An -tx1 -N24 /dev/urandom | tr -d ' \n'; }; \
		sed -e "s|^TRING_JWT_SECRET=.*|TRING_JWT_SECRET=$$(rand)$$(rand)|" \
		    -e "s|^TRING_FS_BASIC_AUTH=.*|TRING_FS_BASIC_AUTH=fs:$$(rand)|" \
		    -e "s|^TRING_ESL_PASSWORD=.*|TRING_ESL_PASSWORD=$$(rand)|" \
		    -e "s|^TRING_REDIS_PASSWORD=.*|TRING_REDIS_PASSWORD=$$(rand)|" \
		    -e "s|^TRING_ADMIN_PASSWORD=.*|TRING_ADMIN_PASSWORD=$$(rand)|" \
		    .env.example > .env && \
		echo "created .env from .env.example with random secrets — review it"; }

down:
	docker compose down

logs:
	docker compose logs -f --tail=20

images:
	docker compose build api gateway freeswitch

# Host-run binaries load .env so they share the containers' secrets.
api: .env
	set -a; . ./.env; set +a; go run ./cmd/api

gateway: .env
	set -a; . ./.env; set +a; go run ./cmd/gateway

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

# Combined web-calling demo (anonymous "call us" + logged-in support + agent
# CRM) — a Flask app on :5000. Set TRING_API_KEY (+ TRING_FLOW_ID) first; see
# examples/demo-portal/README.md for the full env + walkthrough.
demo:
	cd examples/demo-portal && pip install -q -r requirements.txt && python3 app.py

clean:
	rm -rf sdk/js/dist sdk/js/node_modules
	go clean -cache
