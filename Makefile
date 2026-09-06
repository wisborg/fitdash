# fitdash -- the standard way to work with this code.
#
# Every target here DELEGATES to scripts/fd rather than spelling out its own
# `go build` or `go test`. That is the whole point: fd exists so each job is
# one command string, and a Makefile that reimplemented those commands would
# be a second spelling of every one of them -- free to drift, and twice as
# much to change when a flag moves. Read scripts/fd if you want to know what
# a target actually runs.
#
# Unlike videofx, which this borrows its vocabulary from, fitdash is pure Go:
# no cgo, no OpenCV, no pkg-config, so there is no environment to export here.
# ffmpeg is the one external program, used only to encode finished frames, and
# `make check-deps` is what confirms it.

FD := ./scripts/fd

# FIT is the activity to render. Leave it unset and fd falls back to FD_FIT in
# .scratch/env, which is where a local default belongs -- it points at a real
# recording, and this repository is public.
FIT ?=

# ARGS passes flags straight through, e.g.
#   make frames ARGS="--gauges balance --theme light"
ARGS ?=

# PKG and RUN narrow `make test`, e.g.
#   make test PKG=./internal/panel/ RUN=Balance
PKG ?=
RUN ?=

.DEFAULT_GOAL := help
.PHONY: help build test gates frames render diff check-deps clean

help:
	@echo 'fitdash -- make targets (all delegate to scripts/fd)'
	@echo
	@echo '  make build                 build ./fitdash'
	@echo '  make gates                 gofmt + vet + test, one status line each'
	@echo '  make test                  all packages'
	@echo '  make test PKG=./internal/panel/ RUN=Balance'
	@echo '  make frames FIT=ACT.fit    single PNG frames -> .scratch/ (fast visual loop)'
	@echo '  make render FIT=ACT.fit    end-to-end video -> .scratch/'
	@echo '  make diff                  working diff incl. untracked -> .scratch/'
	@echo '  make check-deps            verify ffmpeg is on PATH'
	@echo '  make clean                 empty .scratch/ and remove ./fitdash'
	@echo
	@echo 'FIT is optional once FD_FIT is set in .scratch/env.'
	@echo 'Pass extra flags with ARGS="--theme light".'
	@echo
	@echo 'There is no separate `vet` target: gates runs gofmt, vet and the'
	@echo 'tests together, which is the check to run before committing.'

build:
	@$(FD) build

# gates is the one to run before committing. It covers what videofx splits
# across `vet` and `test`, plus gofmt, and prints one status line each.
gates:
	@$(FD) gates

# PKG and RUN are positional to fd, so a RUN with no PKG would hand fd the
# pattern where it expects a package. Supply ./... in that case.
test:
	@$(FD) test $(or $(PKG),$(if $(RUN),./...)) $(RUN)

# FIT is quoted because this project's own activities have spaces in their
# names, and omitted entirely when unset -- passing an empty argument would
# put "" in front of the flags, where fd expects either a path or nothing.
frames:
	@$(FD) frames $(if $(FIT),"$(FIT)") $(ARGS)

render:
	@$(FD) render $(if $(FIT),"$(FIT)") $(ARGS)

diff:
	@$(FD) diff

check-deps:
	@$(FD) check-deps

# clean does BOTH halves, deliberately. videofx's `clean` removes its binary;
# fd's `clean` empties the scratch directory and keeps .scratch/env, which
# holds local defaults pointing at private recordings. Doing only one of them
# under a name that means the other in the neighbouring repository is how a
# session ends up believing it has a clean tree when it has not.
clean:
	@$(FD) clean
	@rm -f fitdash
