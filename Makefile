Version := $(shell git describe --tags --dirty)
# Version := "dev"
GitCommit := $(shell git rev-parse HEAD)
LDFLAGS := "-s -w -X main.Version=$(Version) -X main.GitCommit=$(GitCommit)"


SERVER?=ghcr.io
OWNER?=openfaas
IMG_NAME?=classic-watchdog
TAG?=$(Version)

.PHONY: all
all: gofmt test dist hashgen

.PHONY: test
test: 
	go test -v ./...

.PHONY: hashgen
hashgen: 
	./ci/hashgen.sh

.PHONY: gofmt
gofmt:
	@echo "+ $@"
	@gofmt -l -d $(shell find . -type f -name '*.go' -not -path "./vendor/*")

.PHONY: dist
dist: 
	CGO_ENABLED=0 GOOS=linux go build -mod=vendor -a -ldflags $(LDFLAGS) -installsuffix cgo -o bin/fwatchdog-amd64
	GOARM=7 GOARCH=arm CGO_ENABLED=0 GOOS=linux go build -mod=vendor -a -ldflags $(LDFLAGS) -installsuffix cgo -o bin/fwatchdog-arm
	GOARCH=arm64 CGO_ENABLED=0 GOOS=linux go build -mod=vendor -a -ldflags $(LDFLAGS) -installsuffix cgo -o bin/fwatchdog-arm64
	GOOS=windows CGO_ENABLED=0 go build -mod=vendor -a -ldflags $(LDFLAGS) -installsuffix cgo -o bin/fwatchdog.exe
	GOOS=darwin CGO_ENABLED=0 go build -mod=vendor -a -ldflags $(LDFLAGS) -installsuffix cgo -o bin/fwatchdog-darwin

# Example:
# SERVER=docker.io OWNER=alexellis2 TAG=ready make publish
.PHONY: publish
publish:
	@echo  $(SERVER)/$(OWNER)/$(IMG_NAME):$(TAG) && \
	docker buildx create --use --name=multiarch --node=multiarch && \
	docker buildx build \
		--platform linux/amd64,linux/arm/v7,linux/arm64 \
		--push=true \
		--tag $(SERVER)/$(OWNER)/$(IMG_NAME):$(TAG) \
		.