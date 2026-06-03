TEST?=./...
TESTTIMEOUT=180m

.EXPORT_ALL_VARIABLES:
	TF_SCHEMA_PANIC_ON_ERROR=1

default: build

build: fmt
	go install

fmt:
	@echo "==> Fixing source code with gofmt..."
	gofmt -s -w .

fmtcheck:
	@echo "==> Checking that code complies with gofmt requirements..."
	@if [ -n "$$(gofmt -l .)" ]; then \
		echo "gofmt needs running on the following files:"; \
		gofmt -l .; \
		echo "You can use the command: \`make fmt\` to reformat code."; \
		exit 1; \
	fi

generate:
	@echo "==> Generating documentation..."
	go generate ./tools

test: fmtcheck
	go test $(TEST) -v $(TESTARGS) -timeout $(TESTTIMEOUT)

testacc: fmtcheck
	TF_ACC=1 go test -tags=acceptance $(TEST) -v $(TESTARGS) -timeout $(TESTTIMEOUT)

vet:
	@echo "==> Running go vet..."
	@go vet ./...

deps:
	@echo "==> Installing dependencies..."
	@go mod download

tidy:
	@echo "==> Tidying go mod..."
	@go mod tidy

.PHONY: build test testacc vet fmt fmtcheck deps tidy generate