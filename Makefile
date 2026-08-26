GO ?= go
export GOTOOLCHAIN ?= local

IMAGE ?= orientation-enrollment-platform
PLATFORM ?= linux/amd64

.PHONY: help build run test race vet fmt check docker-build docker-run tidy clean

help:
	@echo "build        compile every package"
	@echo "run          start the HTTP server locally"
	@echo "test         run the full test suite once"
	@echo "race         run the full test suite under the race detector"
	@echo "vet          run go vet"
	@echo "fmt          format every Go file"
	@echo "check        vet + build + test + race"
	@echo "docker-build build the image for PLATFORM (default linux/amd64)"
	@echo "docker-run   run the image and expose port 8080"

build:
	$(GO) build ./...

run:
	$(GO) run ./cmd/server

test:
	$(GO) test ./... -count=1

race:
	$(GO) test -race ./... -count=1

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

check: vet build test race

tidy:
	$(GO) mod tidy

docker-build:
	docker build --platform $(PLATFORM) -t $(IMAGE):$(subst /,-,$(PLATFORM)) .

docker-run:
	docker run --rm -p 8080:8080 $(IMAGE):$(subst /,-,$(PLATFORM))

clean:
	rm -rf bin data
