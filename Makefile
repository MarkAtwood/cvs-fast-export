# Makefile for cvs-fast-export
#
# Build requirements:  Read the "buildprep" script.
# You may be able to run that to install all dependencies.
#
# You will see some meaningless failures with git 1.7.1 and older.
#
# Note: the reason for the .adoc extensions on README/NEWS/TODO is so they'll
# display nicely in GitLab's repository-browsing interface.
#
# SPDX-FileCopyrightText: (C) Eric S. Raymond <esr@thyrsus.com>
# SPDX-License-Identifier: BSD-2-Clause

PREFIX      ?= /usr/local
BINDIR      ?= $(PREFIX)/bin
DATADIR     ?= $(PREFIX)/share
MANDIR      ?= $(DATADIR)/man

VERSION=$(shell sed -n <NEWS.adoc '/::/s/^\([0-9][^:]*\).*/\1/p' | head -1)

# Rules

# Note: to suppress the footers with timestamps being generated in HTML,
# we use "-a nofooter".
# To debug asciidoc problems, you may need to run "xmllint --nonet --noout --valid"
# on the intermediate XML that throws an error.
.SUFFIXES: .html .adoc .1

.adoc.1:
	asciidoctor -D. -a nofooter -b manpage $<
.adoc.html:
	asciidoctor -D. -a nofooter -a webfonts! $<

.PHONY: all man html clean check pylint cleancheck reflow fixme spellcheck
.PHONY: install uninstall version distclean dist release refresh

# Build

all: cvs-fast-export man html

GOFLAGS=-ldflags='-X main.version=$(VERSION)'
cvs-fast-export: cvs-fast-export.go go.mod go.sum
	go build $(GOFLAGS) -o cvs-fast-export cvs-fast-export.go

rcs-refresh:
	go get github.com/arran4/golang-rcs

reporting-bugs.html: reporting-bugs.adoc
	asciidoctor reporting-bugs.adoc

man: cvs-fast-export.1 cvssync.1 cvsconvert.1

html: cvs-fast-export.html cvssync.html cvsconvert.html reporting-bugs.html

clean:
	rm -f cvs-fast-export
	rm -f *.1 *.html *.tar.gz

# Validate

pylint:
	@pylint --score=n cvssync cvsconvert cvsstrip
	@pylint --score=n tests/*.py

# Warning: The regression tests will fail spuriously if your CVS lacks the
# MirOS patches.  These are carried by Debian Linux and derivatives; you can
# check by Looking for "MirDebian" in the output of cvs --version.
check: cvs-fast-export
	@-shellcheck -f gcc buildprep tests/visualize tests/incremental.sh
	@go vet cvs-fast-export.go
	@cd tests >/dev/null; $(MAKE) --quiet

# Like check, but forces rebuild of the generated test repositories first
cleancheck:
	@$(MAKE) -s -C tests clean
	@$(MAKE) -s check

# Needs clean first because reflowing generated .c and .h files produces ungappy results
reflow: clean
	gofmt -s -d cvs-fast-export.go | patch -p0

fixme:
	@if command -v rg; then \
		rg --no-heading FIX''ME; \
	else \
		find . -type f -exec grep -n FIX''ME {} /dev/null \; | grep -v "[.]git"; \
	fi

# This is eveything except the promt journal
CHECKABLE = \
	cvs-collation-spec.adoc \
	cvsconvert.adoc \
	cvs-fast-export.adoc \
	cvssync.adoc \
	hacking.adoc \
	NEWS.adoc \
	README.adoc \
	reporting-bugs.adoc \
	TODO.adoc
spellcheck:
	@spellcheck local.dic $(CHECKABLE)

# Install/uninstall

install:
	install -d $(DESTDIR)$(BINDIR)
	install -m 755 cvs-fast-export $(DESTDIR)$(BINDIR)/cvs-fast-export
	install cvs-fast-export.1 $(DESTDIR)$(MANDIR)/man1/cvs-fast-export.1

uninstall:
	rm -f $(DESTDIR)$(BINDIR)/cvs-fast-export
	rm -f $(DESTDIR)$(MANDIR)/man1/cvs-fast-export.1

# Export

version:
	@echo $(VERSION)

# Because we don't want copies of the test repositories in the distribution.
distclean: clean
	cd tests; $(MAKE) --quiet clean

SOURCES = Makefile *.[ch] *.[yl] cvssync cvsconvert cvsstrip buildprep
DOCS = control *.adoc local.dic cfe-logo.png
ALL =  $(SOURCES) $(DOCS) tests

cvs-fast-export-$(VERSION).tar.gz: $(SOURCES)
	mkdir cvs-fast-export-$(VERSION)
	cp -r $(ALL) cvs-fast-export-$(VERSION)
	tar -czf cvs-fast-export-$(VERSION).tar.gz cvs-fast-export-$(VERSION)
	rm -fr cvs-fast-export-$(VERSION)
	ls -l cvs-fast-export-$(VERSION).tar.gz

dist: distclean cvs-fast-export-$(VERSION).tar.gz

release: cvs-fast-export-$(VERSION).tar.gz html
	shipper version=$(VERSION) | sh -e -x

refresh: html
	shipper -N -w version=$(VERSION) | sh -e -x

# end
