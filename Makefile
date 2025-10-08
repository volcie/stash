# Stash Backup Tool Makefile
# Cross-platform build automation for Go

# Application info
APP_NAME := stash
VERSION := $(shell git describe --tags --abbrev=0 2>/dev/null || echo "v0.0.0")
COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_TIME := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")

# Build directories
BUILD_DIR := build
DIST_DIR := dist

# Go build flags
LDFLAGS := -ldflags "-X github.com/volcie/stash/cmd.version=$(VERSION) -w -s"
GOFLAGS := -trimpath

# Cross-platform build targets
PLATFORMS := \
	windows/amd64 \
	windows/arm64 \
	linux/amd64 \
	linux/arm64 \
	darwin/arm64 \

# Colors for output
RED := \033[31m
GREEN := \033[32m
YELLOW := \033[33m
BLUE := \033[34m
RESET := \033[0m

.PHONY: help
help: ## Show this help message
	@echo "$(BLUE)Stash Backup Tool - Build System$(RESET)"
	@echo ""
	@echo "$(GREEN)Available targets:$(RESET)"
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  $(YELLOW)%-15s$(RESET) %s\n", $$1, $$2}' $(MAKEFILE_LIST)

.PHONY: build
build: ## Build for current platform
	@echo "$(BLUE)Building $(APP_NAME) for current platform...$(RESET)"
	@mkdir -p $(BUILD_DIR)
	go build $(GOFLAGS) $(LDFLAGS) -o $(BUILD_DIR)/$(APP_NAME)$(shell go env GOEXE) .
	@echo "$(GREEN)Build complete: $(BUILD_DIR)/$(APP_NAME)$(shell go env GOEXE)$(RESET)"

.PHONY: build-all
build-all: clean-dist ## Build for all platforms
	@echo "$(BLUE)Building $(APP_NAME) for all platforms...$(RESET)"
	@mkdir -p $(DIST_DIR)
	@$(foreach platform,$(PLATFORMS), \
		echo "$(YELLOW)Building for $(platform)...$(RESET)"; \
		GOOS=$(word 1,$(subst /, ,$(platform))) \
		GOARCH=$(word 2,$(subst /, ,$(platform))) \
		go build $(GOFLAGS) $(LDFLAGS) \
			-o $(DIST_DIR)/$(APP_NAME)-$(VERSION)-$(subst /,-,$(platform))$(shell test "$(word 1,$(subst /, ,$(platform)))" = "windows" && echo ".exe" || echo "") . || exit 1; \
	)
	@echo "$(GREEN)All builds complete in $(DIST_DIR)/$(RESET)"

.PHONY: build-release
build-release: build-all compress ## Build release packages with compression

.PHONY: compress
compress: ## Compress all built binaries
	@echo "$(BLUE)Compressing binaries...$(RESET)"
	@cd $(DIST_DIR) && \
	for binary in $(APP_NAME)-*; do \
		if [ -f "$$binary" ]; then \
			echo "$(YELLOW)Compressing $$binary...$(RESET)"; \
			if echo "$$binary" | grep -q "windows"; then \
				zip "$$binary.zip" "$$binary"; \
			else \
				tar -czf "$$binary.tar.gz" "$$binary"; \
			fi; \
			rm "$$binary"; \
		fi; \
	done
	@echo "$(GREEN)Compression complete$(RESET)"

.PHONY: install
install: build ## Install to GOPATH/bin
	@echo "$(BLUE)Installing $(APP_NAME)...$(RESET)"
	go install $(GOFLAGS) $(LDFLAGS) .
	@echo "$(GREEN)$(APP_NAME) installed to $(shell go env GOPATH)/bin$(RESET)"

.PHONY: clean
clean: ## Clean build artifacts
	@echo "$(BLUE)Cleaning build artifacts...$(RESET)"
	@rm -rf $(BUILD_DIR)
	@go clean
	@echo "$(GREEN)Clean complete$(RESET)"

.PHONY: clean-dist
clean-dist: ## Clean distribution artifacts
	@echo "$(BLUE)Cleaning distribution artifacts...$(RESET)"
	@rm -rf $(DIST_DIR)
	@echo "$(GREEN)Distribution clean complete$(RESET)"

.PHONY: clean-all
clean-all: clean clean-dist ## Clean all artifacts

.PHONY: test
test: ## Run tests
	@echo "$(BLUE)Running tests...$(RESET)"
	go test -v ./...
	@echo "$(GREEN)Tests complete$(RESET)"

.PHONY: fmt
fmt: ## Format code
	@echo "$(BLUE)Formatting code...$(RESET)"
	go fmt ./...
	@echo "$(GREEN)Code formatted$(RESET)"

.PHONY: vet
vet: ## Vet code for issues
	@echo "$(BLUE)Vetting code...$(RESET)"
	go vet ./...
	@echo "$(GREEN)Code vetted$(RESET)"

.PHONY: deps
deps: ## Download and tidy dependencies
	@echo "$(BLUE)Managing dependencies...$(RESET)"
	go mod download
	go mod tidy
	@echo "$(GREEN)Dependencies updated$(RESET)"

.PHONY: check
check: fmt vet test ## Run all checks (format, vet, test)
	@echo "$(GREEN)All checks passed$(RESET)"

.PHONY: run
run: build ## Build and run with arguments (use ARGS="...")
	@echo "$(BLUE)Running $(APP_NAME)...$(RESET)"
	@$(BUILD_DIR)/$(APP_NAME)$(shell go env GOEXE) $(ARGS)

.PHONY: info
info: ## Show build information
	@echo "$(BLUE)Build Information:$(RESET)"
	@echo "  App Name:    $(APP_NAME)"
	@echo "  Version:     $(VERSION)"
	@echo "  Commit:      $(COMMIT)"
	@echo "  Build Time:  $(BUILD_TIME)"
	@echo "  Go Version:  $(shell go version)"
	@echo "  Platform:    $(shell go env GOOS)/$(shell go env GOARCH)"

.PHONY: size
size: build ## Show binary size
	@echo "$(BLUE)Binary Size:$(RESET)"
	@ls -lh $(BUILD_DIR)/$(APP_NAME)$(shell go env GOEXE) | awk '{print "  " $$5 "  " $$9}'

# Default target
.DEFAULT_GOAL := build