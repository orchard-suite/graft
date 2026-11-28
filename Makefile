# Graft: the binary and its image.
#
# `make` builds ./graft for this machine. `make check` is the gate before a
# commit, and `make land` runs it before it lands. CI runs it again on every
# pull request and before every release.
# `make image` builds the deployed image for linux/amd64.

SHELL := /bin/bash
VERSION := $(shell sed -E 's/ .*//' VERSION)
IMAGE ?= graft:$(VERSION)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: default build run run-workspace test lint check land image clean

default: build

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o graft ./cmd/graft

# Local run against ./data, ./repos, and ./work with the local engine
# socket, signed in as the fixed local user. It needs a program file in
# GRAFT_PROGRAM_FILE and the hub's and the forge's settings (docs/runbook.md).
# Start `make run-workspace` beside it.
run: build
	GRAFT_AUTH_MODE=none GRAFT_DATA_DIR=$(CURDIR)/data GRAFT_WORK_DIR=$(CURDIR)/work \
	GRAFT_REPO_DIR=$(CURDIR)/repos ./graft

run-workspace: build
	GRAFT_WORK_DIR=$(CURDIR)/work ./graft workspace

test:
	go test ./...

lint:
	go vet ./...
	gofmt -l . | tee /dev/stderr | test -z "$$(cat)"

check: lint test

# release-damnit runs the config's check on the tree that lands and signs
# its proof. From a worktree: make land BRANCH=<branch>.
land:
	release-damnit land $(BRANCH)

image:
	docker build --platform linux/amd64 -t $(IMAGE) .

clean:
	rm -rf graft data work dist
