
GO_BUILD_ENV :=
GO_BUILD_FLAGS :=
MODULE_BINARY := bin/viam-chess

ifeq ($(VIAM_TARGET_OS), windows)
	GO_BUILD_ENV += GOOS=windows GOARCH=amd64
	GO_BUILD_FLAGS := -tags no_cgo
	MODULE_BINARY = bin/viam-chess.exe
endif

all: $(MODULE_BINARY) cli

cli: *.go cmd/cli/*.go
	go build -o ./chesscli cmd/cli/*.go

$(MODULE_BINARY): Makefile go.mod *.go cmd/module/*.go 
	GOOS=$(VIAM_BUILD_OS) GOARCH=$(VIAM_BUILD_ARCH) $(GO_BUILD_ENV) go build $(GO_BUILD_FLAGS) -o $(MODULE_BINARY) cmd/module/main.go

lint:
	gofmt -s -w .

update:
	go get go.viam.com/rdk@latest
	go get github.com/erh/vmodutils@latest
	go mod tidy

test:
	go test ./...

.PHONY: viamapp-dist

viamapp-dist: viamapp/*.json  viamapp/*.html viamapp/*.html viamapp/src/*.ts viamapp/src/*.css
	cd viamapp && npm run build

module.tar.gz: meta.json $(MODULE_BINARY) viamapp-dist
ifneq ($(VIAM_TARGET_OS), windows)
	strip $(MODULE_BINARY)
endif
	mkdir -p .module-stage/$(dir $(MODULE_BINARY)) .module-stage/dist
	cp meta.json .module-stage/
	cp $(MODULE_BINARY) .module-stage/$(MODULE_BINARY)
	cp -R viamapp/dist/. .module-stage/dist/
	cd .module-stage && tar czf ../$@ bin dist meta.json

module: test module.tar.gz

all: test module.tar.gz

setup:
	go mod tidy
	if ! command -v npm > /dev/null 2>&1; then \
		curl -fsSL https://deb.nodesource.com/setup_20.x | bash - && \
		apt-get install -y nodejs; \
	fi
	cd viamapp && npm install
