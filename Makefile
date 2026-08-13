.PHONY: all daemon test test-race test-cover apk apk-dev apk-clean clean userdata

CURDIR := $(shell pwd)

all: daemon

# go.mod lives in daemon/, so every go command cd's into it first.
daemon:
	cd daemon && go build ./... && go vet ./... && go test ./...

# test runs offline — no API key or network; test-race adds the race detector.
test:
	cd daemon && go build ./... && go vet ./... && go test ./...

test-race:
	cd daemon && go test -race ./...

test-cover:
	cd daemon && go test -cover ./...

# apk-dev skips vet/test for the fastest dev cycle; apk-clean rebuilds from scratch.
apk:
	bash scripts/build.sh

apk-dev:
	bash scripts/build.sh --skip-tests

apk-clean:
	bash scripts/build.sh --clean

# PC-side backup of userdata/ — importable on the phone via Settings → Backup → Import.
userdata:
	python3 scripts/make_userdata.py

clean:
	rm -f openyou-v*.apk
	cd android && ./gradlew clean
