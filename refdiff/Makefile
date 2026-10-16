# The Command Line Tools linker can't read the stray MacOSX27.0.sdk stubs; build against the default SDK.
SDK := $(shell ls -d /Library/Developer/CommandLineTools/SDKs/MacOSX.sdk 2>/dev/null)

refdiff: *.go index.html assets/* queries/*
	SDKROOT=$(SDK) go build -ldflags="-s -w" -o refdiff .

test:
	SDKROOT=$(SDK) go test ./...

.PHONY: test
