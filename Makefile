.PHONY: test race cover fuzz vet fmt check build

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
