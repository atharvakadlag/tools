# The Command Line Tools linker can't read the stray MacOSX27.0.sdk stubs; build against the default SDK.
SDK     := $(shell ls -d /Library/Developer/CommandLineTools/SDKs/MacOSX.sdk 2>/dev/null)
VERSION ?= $(or $(V),$(shell git describe --tags --match 'refdiff-v*' 2>/dev/null | sed 's/^refdiff-v//'),dev)
TARGET  := $(shell go env GOOS)-$(shell go env GOARCH)
TARBALL := dist/refdiff-$(V)-$(TARGET).tar.gz

refdiff: *.go index.html assets/* queries/*
	SDKROOT=$(SDK) go build -ldflags="-s -w -X main.version=$(VERSION)" -o refdiff .

test:
	SDKROOT=$(SDK) go test ./...

# make release V=0.2.0  — tags refdiff-v0.2.0 on the pushed HEAD and uploads the binary as a GitHub Release.
release:
	@test -n "$(V)" || { echo "usage: make release V=x.y.z"; exit 1; }
	@test -z "$$(git status --porcelain -- .)" || { echo "refdiff/ has uncommitted changes"; exit 1; }
	@git fetch -q origin && test "$$(git rev-parse HEAD)" = "$$(git rev-parse @{u})" || { echo "HEAD is not pushed"; exit 1; }
	$(MAKE) -B refdiff VERSION=$(V)
	./refdiff --version
	mkdir -p dist && tar -czf $(TARBALL) refdiff
	printf '%s\n\n%s\n' "Live browser diff viewer for git refs. Built for $(TARGET)." \
	  "Install: download, \`tar -xzf\`, and put \`refdiff\` on your PATH. See refdiff/README.md." > dist/notes.md
	gh release create refdiff-v$(V) $(TARBALL) --target $$(git rev-parse HEAD) --title "refdiff v$(V)" --notes-file dist/notes.md

.PHONY: test release
