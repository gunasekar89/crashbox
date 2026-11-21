VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
IMAGE   ?= ghcr.io/gunasekar89/crashbox:$(VERSION)
LDFLAGS := -s -w -X github.com/gunasekar89/crashbox/internal/collect.Version=$(VERSION)

.PHONY: build test vet image deploy
build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/crashbox ./cmd/crashbox
test:
	go test ./...
vet:
	go vet ./...
image:
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE) .
deploy:
	kubectl apply -f deploy/crashbox.yaml
