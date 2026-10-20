MERMAID := 11.17.2
VERSION ?= $(or $(V),$(shell git describe --tags --match 'mmdview-v*' 2>/dev/null | sed 's/^mmdview-v//'),dev)
TARGETS := darwin-arm64 darwin-amd64 linux-amd64 linux-arm64
LDFLAGS := -s -w -X main.version=$(VERSION)

mmdview: *.go index.html assets/mermaid.min.js
	CGO_ENABLED=0 go build -ldflags="$(LDFLAGS)" -o mmdview .

assets/mermaid.min.js:
	mkdir -p assets
	curl -fsSL -o $@ https://cdn.jsdelivr.net/npm/mermaid@$(MERMAID)/dist/mermaid.min.js

test: assets/mermaid.min.js
	go test ./...

# make release V=0.2.0  — tags mmdview-v0.2.0 on the pushed HEAD and uploads binaries for $(TARGETS) as a GitHub Release.
release: assets/mermaid.min.js
	@test -n "$(V)" || { echo "usage: make release V=x.y.z"; exit 1; }
	@test -z "$$(git status --porcelain -- .)" || { echo "mmdview/ has uncommitted changes"; exit 1; }
	@git fetch -q origin && test "$$(git rev-parse HEAD)" = "$$(git rev-parse @{u})" || { echo "HEAD is not pushed"; exit 1; }
	rm -rf dist && mkdir -p dist
	for t in $(TARGETS); do \
	  mkdir -p dist/$$t && \
	  CGO_ENABLED=0 GOOS=$${t%-*} GOARCH=$${t#*-} go build -ldflags="-s -w -X main.version=$(V)" -o dist/$$t/mmdview . && \
	  tar -czf dist/mmdview-$(V)-$$t.tar.gz -C dist/$$t mmdview || exit 1; \
	done
	printf '%s\n\n%s\n' "Live-reloading Mermaid viewer (Mermaid $(MERMAID) bundled)." \
	  "Install: download, \`tar -xzf\`, and put \`mmdview\` on your PATH. See mmdview/README.md." > dist/notes.md
	gh release create mmdview-v$(V) dist/mmdview-$(V)-*.tar.gz --target $$(git rev-parse HEAD) --title "mmdview v$(V)" --notes-file dist/notes.md

.PHONY: test release
