PKG := github.com/DBN-DEV/skyeye
BUILD_VERSION=$(if $(VERSION),$(VERSION),$(shell git describe --tags --always))
BUILD_COMMIT=$(if $(COMMIT),$(COMMIT),$(shell git rev-parse --short HEAD))
DATE=$(shell date -u '+%Y-%m-%dT%H:%M:%SZ')

LDFLAGS += -X "$(PKG)/version.Version=$(BUILD_VERSION)"
LDFLAGS += -X "$(PKG)/version.Commit=$(BUILD_COMMIT)"
LDFLAGS += -X "$(PKG)/version.Date=$(DATE)"

AGENT_BINARY_NAME := skyeye-agent

test:
	@go test ./...

fmt:
	goimports -w -local github.com/DBN-DEV/skyeye ./.

gen-pb:
	@echo Build proto file
	@protoc --go-grpc_out=./pb --go_out=./pb ./proto/*.proto
	@echo Build proto file done

build: build-agent

build-agent:
	@echo "Building Skyeye agent binary..."
	@echo "Version: $(BUILD_VERSION)"
	@echo "Commit: $(BUILD_COMMIT)"
	@echo "Date: $(DATE)"
	@GO111MODULE=on CGO_ENABLED=0 go build -ldflags '$(LDFLAGS)' -o $(AGENT_BINARY_NAME) ./cmd/agent/main.go
	@echo "Building Skyeye agent done"

