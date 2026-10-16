GO ?= go
VERSION ?= dev
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: all panel agent web test clean

all: web agent panel

panel:
	$(GO) build -o dist/keel-panel ./panel

agent:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -ldflags "$(LDFLAGS)" -o dist/keel-agent-linux-amd64 ./agent
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -ldflags "$(LDFLAGS)" -o dist/keel-agent-linux-arm64 ./agent

web:
	cd web && npm ci && npm run build

# Set KEEL_TEST_DATABASE_URL to also run the panel API tests against PostgreSQL.
test:
	$(GO) vet ./...
	$(GO) test ./...

clean:
	rm -rf dist web/dist
