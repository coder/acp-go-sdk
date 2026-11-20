ARCH := $(shell uname -m)
GO_FILES := $(shell find . -name '*.go' -type f)
# The top-level version file is the SDK's own semver. schema/version pins the
# ACP schema release that cmd/generate reads. Regeneration never writes version.
SCHEMA_VERSION := $(file < ./schema/version)
# Upstream publishes 0.x schemas under v<version> release tags, and 1.0 and
# later under schema-v<version>.
SCHEMA_TAG := $(if $(filter 0,$(firstword $(subst ., ,$(SCHEMA_VERSION)))),v,schema-v)$(SCHEMA_VERSION)
SCHEMA_URL := https://github.com/agentclientprotocol/agent-client-protocol/releases/download/$(SCHEMA_TAG)
SCHEMA_FILES := schema/meta.json schema/schema.json schema/meta.unstable.json schema/schema.unstable.json
GOCACHE ?= $(CURDIR)/.gocache

# Download the schema files when schema/version is newer, then regenerate the
# *_gen.go files from them.
.PHONY: generate
generate: $(SCHEMA_FILES)
	cd cmd/generate && env -u GOPATH -u GOMODCACHE go run .

schema/meta.json: schema/version
	curl -o $@ --fail -L $(SCHEMA_URL)/meta.json

schema/schema.json: schema/version
	curl -o $@ --fail -L $(SCHEMA_URL)/schema.json

schema/meta.unstable.json: schema/version
	@set -e; \
		url=$(SCHEMA_URL)/meta.unstable.json; \
		tmp=$@.tmp; \
		status=$$(curl -sS -L -o "$$tmp" -w '%{http_code}' "$$url") || { rm -f "$$tmp"; exit 1; }; \
		if [ "$$status" = "200" ]; then \
			mv "$$tmp" $@; \
		elif [ "$$status" = "404" ]; then \
			rm -f "$$tmp"; \
			printf '%s\n' '{"agentMethods":{},"clientMethods":{},"protocolMethods":{}}' > $@; \
		else \
			rm -f "$$tmp"; \
			echo "failed to download $$url (http $$status)" 1>&2; \
			exit 1; \
		fi

schema/schema.unstable.json: schema/version
	@set -e; \
		url=$(SCHEMA_URL)/schema.unstable.json; \
		tmp=$@.tmp; \
		status=$$(curl -sS -L -o "$$tmp" -w '%{http_code}' "$$url") || { rm -f "$$tmp"; exit 1; }; \
		if [ "$$status" = "200" ]; then \
			mv "$$tmp" $@; \
		elif [ "$$status" = "404" ]; then \
			rm -f "$$tmp"; \
			printf '%s\n' '{"$$defs":{}}' > $@; \
		else \
			rm -f "$$tmp"; \
			echo "failed to download $$url (http $$status)" 1>&2; \
			exit 1; \
		fi

.PHONY: fmt
fmt:
	treefmt

# treefmt runs mdsh + mdformat over the markdown, which keeps README.md
# regenerated and in sync (replacing the old mdsh guard). mdsh and mdformat
# disagree on the blank line after an mdsh directive: mdsh strips it, mdformat
# re-adds it. The net result is a no-op (README is a fixpoint of the pair), but
# that intermediate churn trips treefmt's own --fail-on-change. So we format in
# place and let git confirm there's no net drift instead.
.PHONY: check
check:
	treefmt
	git diff --exit-code

.PHONY: test
test: $(GO_FILES)
	GOFLAGS=$(GOFLAGS) GOCACHE=$(GOCACHE) go vet ./...
	GOFLAGS=$(GOFLAGS) GOCACHE=$(GOCACHE) go test ./...
	GOFLAGS=$(GOFLAGS) GOCACHE=$(GOCACHE) go build ./example/...
	# cmd/generate is its own module, so ./... above does not reach it.
	cd cmd/generate && GOFLAGS=$(GOFLAGS) GOCACHE=$(GOCACHE) go vet ./...
	cd cmd/generate && GOFLAGS=$(GOFLAGS) GOCACHE=$(GOCACHE) go test ./...
	# ./... skips dot directories, so name the verification drivers directly.
	GOFLAGS=$(GOFLAGS) GOCACHE=$(GOCACHE) go vet ./.agents/skills/verify-real-harnesses/scripts/acpharness/
	GOFLAGS=$(GOFLAGS) GOCACHE=$(GOCACHE) go test ./.agents/skills/verify-acp-go-sdk/scripts/acpwire/ ./.agents/skills/verify-real-harnesses/scripts/acpharness/
	# verify-schema-bump is its own module. Its roundtrip package compiles
	# only with the registry that registrygen writes (git ignores it). The
	# G2 gate checks (TestTranscripts, TestInstances, TestHints) stay in
	# scripts/g2-roundtrip; every other test here is an offline unit test.
	cd .agents/skills/verify-schema-bump && GOFLAGS=$(GOFLAGS) GOCACHE=$(GOCACHE) go run ./registrygen -sdk ../../.. -out roundtrip/registry_gen_test.go >/dev/null
	cd .agents/skills/verify-schema-bump && GOFLAGS=$(GOFLAGS) GOCACHE=$(GOCACHE) go vet ./...
	cd .agents/skills/verify-schema-bump && GOFLAGS=$(GOFLAGS) GOCACHE=$(GOCACHE) go test -skip '^(TestTranscripts|TestInstances|TestHints)$$' ./...

.PHONY: clean
clean:
	rm -f $(SCHEMA_FILES)

# Prepare a release PR: write the SDK version to version (and the schema pin to
# schema/version when SCHEMA is set), regenerate, format, and test. It makes no
# commit or tag. RELEASING.md lists the manual steps that follow.
.PHONY: release
release:
	@printf '%s\n' "$(VERSION)" | grep -Eqx '[0-9]+\.[0-9]+\.[0-9]+' || { \
		echo "VERSION must be the SDK semver without a v (e.g. 'make release VERSION=0.14.0 SCHEMA=1.18.0')" 1>&2; \
		exit 1; \
	}
	@[ -z "$(SCHEMA)" ] || printf '%s\n' "$(SCHEMA)" | grep -Eqx '[0-9]+\.[0-9]+\.[0-9]+' || { \
		echo "SCHEMA must be an ACP schema version without a v (e.g. 1.18.0)" 1>&2; \
		exit 1; \
	}
	@printf '%s\n' "$(VERSION)" > version
	@[ -z "$(SCHEMA)" ] || printf '%s\n' "$(SCHEMA)" > schema/version
	$(MAKE) generate
# The treefmt cache would skip README.md, whose install line mdsh builds
# from version.
	TREEFMT_NO_CACHE=1 $(MAKE) fmt
	GOCACHE=$(GOCACHE) $(MAKE) test
	@echo
	@echo "Release candidate v$(VERSION) (ACP schema $$(cat schema/version)) is ready."
	@echo "Commit it, run 'make check', and open a PR. After it merges, run the Release workflow (see RELEASING.md)."
