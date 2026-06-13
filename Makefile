GOOGLESQL_WASM_REPO     ?= goccy/googlesql-wasm
GOOGLESQL_WASM_VERSION  ?= v0.3.1
GOOGLESQL_WASM_WORKFLOW ?= goccy/googlesql-wasm/.github/workflows/build.yml

BRIDGE_ASSET     := googlesql_wasm2go.go
BRIDGE_FILE      := googlesql.go
UPSTREAM_DIR     ?= .upstream
UPSTREAM_BRIDGE_FILE := $(UPSTREAM_DIR)/$(BRIDGE_FILE)
RELEASE_URL       = https://github.com/$(GOOGLESQL_WASM_REPO)/releases/download/$(GOOGLESQL_WASM_VERSION)
ATTESTATION_API   = https://api.github.com/repos/$(GOOGLESQL_WASM_REPO)/attestations

.PHONY: googlesql download-upstream verify-upstream verify-patch download verify test

## googlesql: fetch the upstream wasm2go bridge into .upstream/
## and verify its GitHub artifact attestation. This target is
## intentionally non-destructive: the checked-in googlesql.go carries
## downstream bridge patches and must not be overwritten by default.
googlesql: download-upstream verify-upstream

## download-upstream: fetch the wasm2go-runtime bridge from the upstream
## release without replacing this fork's patched bridge.
download-upstream:
	mkdir -p $(UPSTREAM_DIR)
	curl -fSL --proto '=https' --tlsv1.2 -o $(UPSTREAM_BRIDGE_FILE) $(RELEASE_URL)/$(BRIDGE_ASSET)

## verify-upstream: confirm the downloaded upstream bridge carries a valid
## attestation signed by the upstream build.yml workflow. The
## attestation bundle is fetched anonymously from the public
## attestation API and verified offline via
## `gh attestation verify --bundle`. No GH access token is required.
verify-upstream:
	@set -eu; \
	test -f "$(UPSTREAM_BRIDGE_FILE)"; \
	tmpdir=$$(mktemp -d); \
	response=$$tmpdir/attestations.json; \
	bundle=$$tmpdir/bundle.jsonl; \
	trap 'rm -rf $$tmpdir' EXIT; \
	digest=$$(shasum -a 256 $(UPSTREAM_BRIDGE_FILE) | awk '{print $$1}'); \
	echo "==> fetching attestation bundle for $(UPSTREAM_BRIDGE_FILE) (sha256:$$digest)"; \
	curl -fsSL --proto '=https' --tlsv1.2 \
	  "$(ATTESTATION_API)/sha256:$$digest" -o $$response; \
	jq -e '.attestations | length > 0' $$response >/dev/null \
	  || { echo "no upstream attestation found for sha256:$$digest" >&2; exit 1; }; \
	jq -c '.attestations[].bundle' $$response > $$bundle; \
	echo "==> verifying $(UPSTREAM_BRIDGE_FILE)"; \
	GH_TOKEN= GITHUB_TOKEN= gh attestation verify "$(UPSTREAM_BRIDGE_FILE)" \
	  -R $(GOOGLESQL_WASM_REPO) \
	  --bundle $$bundle \
	  --signer-workflow $(GOOGLESQL_WASM_WORKFLOW)

download: download-upstream
verify: verify-upstream

## test: run the Go test suite.
test:
	go test -short ./...

## verify-patch: prove googlesql.go == the upstream bridge baseline plus the
## downstream patch set in patches/, byte for byte. No network: it uses the
## 'upstream' git ref the patches were generated against. If missing, run
## `git remote add upstream https://github.com/goccy/go-googlesql && git fetch upstream`.
verify-patch:
	@set -eu; \
	git rev-parse --verify -q upstream/main >/dev/null \
	  || { echo "need upstream/main: git remote add upstream https://github.com/goccy/go-googlesql && git fetch upstream" >&2; exit 1; }; \
	tmp=$$(mktemp -d); trap 'rm -rf $$tmp' EXIT; \
	git show upstream/main:$(BRIDGE_FILE) > $$tmp/$(BRIDGE_FILE); \
	( cd $$tmp && git apply -p1 $(CURDIR)/patches/*.patch ); \
	if diff -q $$tmp/$(BRIDGE_FILE) $(BRIDGE_FILE) >/dev/null; then \
	  echo "verify-patch OK: $(BRIDGE_FILE) == upstream/main + patches/"; \
	else \
	  echo "verify-patch FAIL: bridge diverges from baseline + patches/" >&2; exit 1; \
	fi
