.PHONY: build build-image lint vet fmt test test-e2e release

NIX ?= nix

## build: build the Go server binary into bin/sumomo
build:
	$(NIX) develop -c sh -c 'cd server && go build -o ../bin/sumomo .'

## build-image: build the native-architecture container image archive
build-image:
	$(NIX) build .#sumomo-image --print-out-paths --no-link

## lint: golangci-lint the server sources
lint:
	$(NIX) develop -c sh -c 'cd server && golangci-lint run'

## vet: go vet the server sources
vet:
	$(NIX) develop -c sh -c 'cd server && gofmt -l . && go vet ./...'

## fmt: gofmt the server sources
fmt:
	$(NIX) develop -c sh -c 'cd server && gofmt -w .'

## test: run the server unit tests
test:
	$(NIX) develop -c sh -c 'cd server && go test ./...'

## test-e2e: deploy Mastodon on kind with sumomo as its translation
## backend and verify translation through Mastodon's API
test-e2e:
	$(NIX) develop .#e2e -c bash scripts/e2e.sh

## release: tag VERSION (e.g. 0.1.0; a leading "v" is optional and
## added if missing), push the tag, and create a GitHub release.
## Pushing the tag triggers CI to publish the multi-arch (amd64+arm64)
## image to ghcr.io/ushitora-anqou/sumomo.
release:
	@test -n "$(VERSION)" || (echo "usage: make release VERSION=X.Y.Z" >&2; exit 2)
	@TAG="v$$(printf '%s' "$(VERSION)" | sed 's/^v//')"; \
	git tag "$$TAG"; \
	git push origin "$$TAG"; \
	gh release create "$$TAG" --title "$$TAG" --generate-notes
