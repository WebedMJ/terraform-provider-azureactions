TEST?=$$(go list ./... | grep -v 'vendor')
TESTTIMEOUT=180m

.EXPORT_ALL_VARIABLES:
	TF_SCHEMA_PANIC_ON_ERROR=1

default: build

build: fmt
	go install

fmt:
	@echo "==> Fixing source code with gofmt..."
	find . -name '*.go' | grep -v vendor | xargs gofmt -s -w

fmtcheck:
	@sh -c "'$(CURDIR)/scripts/gofmtcheck.sh'"

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