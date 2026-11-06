.PHONY: build vet fmt test-e2e release

NIX ?= nix

## build: build the amd64 container image archive
build:
	$(NIX) build .#sumomo-image --print-out-paths --no-link

## vet: go vet the server sources
vet:
	$(NIX) develop -c sh -c 'cd server && gofmt -l . && go vet ./...'

## fmt: gofmt the server sources
fmt:
	$(NIX) develop -c sh -c 'cd server && gofmt -w .'

## test-e2e: deploy Mastodon on kind with sumomo as its translation
## backend and verify translation through Mastodon's API
test-e2e:
	$(NIX) develop .#e2e -c bash scripts/e2e.sh

## release: tag VERSION (e.g. v0.1.0), push the tag, and create a
## GitHub release. Pushing the tag triggers CI to publish the
## multi-arch (amd64+arm64) image to ghcr.io/ushitora-anqou/sumomo.
release:
	@test -n "$(VERSION)" || (echo "usage: make release VERSION=vX.Y.Z" >&2; exit 2)
	git tag "$(VERSION)"
	git push origin "$(VERSION)"
	gh release create "$(VERSION)" --title "$(VERSION)" --generate-notes
