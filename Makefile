REQUIRED_TOOLS = goreleaser
K := $(foreach exec,$(REQUIRED_TOOLS),\
        $(if $(shell which $(exec)),some string,$(error "No $(exec) in PATH")))

.PHONY: build
build:
	goreleaser build --snapshot --clean
