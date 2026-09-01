.PHONY: test race cover fuzz vet fmt check build demo integration

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

fmt:
	gofmt -l -w .

check: vet race cover

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
	go test -tags integration -count=1 -v ./internal/integration
