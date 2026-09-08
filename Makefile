.PHONY: test race cover fuzz vet fmt check build demo integration linux snapshot check-dist

test:
	go test ./...

race:
	go test -race ./...

cover:
	go test -coverprofile=coverage.out ./internal/...
	go tool cover -func=coverage.out | tail -1
	@echo "run 'go tool cover -html=coverage.out' for the annotated source"

# The round-trip invariant is the one that protects the user's ssh config.
fuzz:
	go test ./internal/sshconf -run=XXX -fuzz=FuzzRoundTrip -fuzztime=60s

vet:
	go vet ./...
	go vet -tags integration ./internal/integration

fmt:
	gofmt -l -w .

# Linux is a first-class target (SASASH.6): a darwin-only regression must
# not land, so the gate cross-builds and vets for it on every check.
linux:
	GOOS=linux go build ./...
	GOOS=linux go vet ./...
	GOOS=linux go vet -tags integration ./internal/integration

check: vet linux race cover

build:
	go build -o sasash ./cmd/sasash

# Renders demo/demo.tape against the RFC 5737 fixture in demo/home.
# Review aid only, never a CI gate: skips (exit 0) when vhs is not installed.
demo: build
	@if command -v vhs >/dev/null 2>&1; then \
		vhs demo/demo.tape && cp demo/out/demo.gif demo/demo.gif; \
	else \
		echo "vhs not installed; skipping demo (brew install vhs)"; \
	fi

# Real-sshd tier (SPEC §12): proves -O forward/cancel/check/exit against a
# live OpenSSH. Run locally before a release, not on every save; skips when
# no sshd binary is installed (brew install openssh).
integration:
	go test -tags integration -count=1 -timeout 120s -v ./internal/integration

# Distribution (SASASH.8). Both need brew-installed tools (goreleaser, shellcheck,
# actionlint); ci.yml runs the same on every push. Neither is part of `check`.
# snapshot: build all four release targets into dist/ exactly as a tag would,
# without a tag or a push.
snapshot:
	goreleaser release --snapshot --clean

# check-dist: lint the workflows and the installer, then run the installer
# end-to-end against the snapshot's real artifacts over file:// (no network) —
# which is what keeps install.sh's asset naming honest to .goreleaser.yaml.
check-dist: snapshot
	shellcheck install.sh scripts/*.sh
	actionlint
	scripts/test-install.sh
