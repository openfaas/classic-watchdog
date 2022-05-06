Version := $(shell git describe --tags --dirty)
# Version := "dev"
GitCommit := $(shell git rev-parse HEAD)
LDFLAGS := "-s -w -X main.Version=$(Version) -X main.GitCommit=$(GitCommit)"
GO111MODULE=on
SOURCE_DIRS := $(shell find . -name '*.go' |grep -v vendor)

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
	@test -z $(shell gofmt -l -s $(SOURCE_DIRS) | tee /dev/stderr) || (echo "[WARN] Fix formatting issues with 'make gofmt'" && exit 1)

.PHONY: dist
dist: 
	CGO_ENABLED=0 GOOS=linux go build -mod=vendor -a -ldflags $(LDFLAGS) -installsuffix cgo -o bin/fwatchdog-amd64
	GOARM=7 GOARCH=arm CGO_ENABLED=0 GOOS=linux go build -mod=vendor -a -ldflags $(LDFLAGS) -installsuffix cgo -o bin/fwatchdog-arm
	GOARCH=arm64 CGO_ENABLED=0 GOOS=linux go build -mod=vendor -a -ldflags $(LDFLAGS) -installsuffix cgo -o bin/fwatchdog-arm64
	GOOS=windows CGO_ENABLED=0 go build -mod=vendor -a -ldflags $(LDFLAGS) -installsuffix cgo -o bin/fwatchdog.exe
	GOOS=darwin CGO_ENABLED=0 go build -mod=vendor -a -ldflags $(LDFLAGS) -installsuffix cgo -o bin/fwatchdog-darwin
