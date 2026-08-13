POSTGREST_VERSION = v12.2.3

REGISTRY = eu.gcr.io/divine-arcade-94510
REPO = goserver
TAG = $(shell cat version.go | grep VERSION | grep -oP "(\d+\.?)+" )
IMAGE_DST = $(REGISTRY)/$(REPO):$(TAG)
gofiles = $(shell find . -type d -name node_modules -prune -o -name '*.go' -o -name '*.ts' -o -name '*.tsx' -o -name '*.html' -o -name '*.css') Makefile go.mod

webfiles = $(shell find web -type d -name node_modules -prune -o -type f -name '*.ts' -o -name '*.tsx')
patfiles = $(shell find . -type f -name '*.pat')

PATH := $(PATH):web/node_modules/.bin

.PHONY: test server

all: server

_templates: $(patfiles) patron.ts
	./patron.ts $(patfiles)
	touch _templates

web/static/test.js: $(webfiles)
	bun build web/test.tsx --bundle --outdir=web/static --loader:.css=text --minify --sourcemap

server: $(gofiles) web/static/test.js _templates
	@# nilaway .
	@# staticcheck -f stylish .
	GOAMD64=v2 GOOS=linux CGO_ENABLED=0 go build -ldflags="-s -w"

watch:
	while inotifywait -qre close_write . ; do make server --no-print-directory ; done

test: test-cleanup
	cd test && bun test --bail=1

test-cleanup:
	[ "$(shell docker ps -a -q --filter "label=com.sales-way.test")" != "" ] && docker rm -f $(shell docker ps -a -q --filter "label=com.sales-way.test") || echo "No test containers to cleanup"

reset:
	rm server

image: server
	cp /etc/ssl/certs/ca-certificates.crt .
	docker build --no-cache --rm -t "$(IMAGE_DST)" .

upload: image
	#gcloud docker -a
	docker push $(IMAGE_DST)
