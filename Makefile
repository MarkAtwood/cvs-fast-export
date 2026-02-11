# Makefile for cvs-fast-export
#
# Build requirements:  Read the "buildprep" script.
# You may be able to run that to install all dependencies.
#
# The C compiler must be C99-conformant, and support anonymous unions
# (GNU, clang, C11).
#
# If you get a link error complaining that clock_gettime() can't be
# found, uncomment the line that includes -lrt in the link flags.
# You'll need this if your glibc version is < 2.17; that is for RHEL 6
# or older and Debian Wheezy or older.  Alas, we can't unconditionally
# include -lrt because Darwin.
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

INSTALL = install
TAR ?= tar
BISON ?= bison
FLEX ?= flex

VERSION = $(shell sed -n <NEWS.adoc '/::/s/^\([^:]*\).*/\1/p' | head -1)

GCC_WARNINGS1=-Wall -Wextra -Werror -Wpointer-arith -Wstrict-prototypes
GCC_WARNINGS3=-Wno-format-zero-length
GCC_WARNINGS=$(GCC_WARNINGS1) $(GCC_WARNINGS2) $(GCC_WARNINGS3)
CFLAGS += $(GCC_WARNINGS)
CPPFLAGS += -I.
#LIBS=-lrt
CPPFLAGS += -DVERSION=\"$(VERSION)\"

# Enable this for multithreading.
CFLAGS += -pthread
CPPFLAGS += -DTHREADS

# Optimizing for speed. Un-comment for testing Linux build
#CFLAGS += -march=native

# Compile in debugging code
#CPPFLAGS += -DDEBUG
# To enable debugging of the Yacc grammar, uncomment the following line
#CPPFLAGS += -DYYDEBUG=1
# To enable verbose debugging of the analysis stage, uncomment the following line
#CPPFLAGS += -DCVSDEBUG=1
# To enable debugging of blob export, uncomment the following line
#CPPFLAGS += -DFDEBUG=1
# To enable assertions of red black trees, uncomment the following line
#CPPFLAGS += -DRBDEBUG=1
# To enable debugging of order instability issues
#CPPFLAGS += -DORDERDEBUG=1
# To enable debugging of gitspace backlinks, uncomment the following line
#CPPFLAGS += -DGITSPACEDEBUG=1

# To enable profiling, uncomment the following line
# Note: the profiler gets confused if you don't also turn off -O flags.
# CFLAGS += -pg
# Warning: Using -O3 has been seen to cause core dumps on repositories with
# very long revision names - some bounds check gets optimized away. Don't do that.
CFLAGS += -O2
# If your toolchain supports link time optimization this is a cheap speedup
# CFLAGS += -flto
# Bounds checks at runtime. Not enabled by default for performance reasons
#CPPLAGS += -D_FORTIFY_SOURCE=2
# Ordinary symbolic debugging
#CFLAGS += -g
# Test coverage flags
# CFLAGS += -ftest-coverage -fprofile-arcs
CFLAGS += $(EXTRA_CFLAGS)

#YFLAGS= --report=all
LFLAGS=

OBJS=gram.o lex.o rbtree.o main.o import.o dump.o cvsnumber.o \
	cvsutil.o revdir.o revlist.o atom.o revcvs.o generate.o export.o \
	nodehash.o tags.o authormap.o graph.o utils.o collate.o hash.o

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

.PHONY: all man html clean check cppcheck pylint cleancheck reflow fixme spellcheck
.PHONY: install uninstall version distclean dist release refresh

all: cvs-fast-export man html

cvs-fast-export: $(OBJS)
	$(CC) $(CFLAGS) $(TARGET_ARCH) $(OBJS) $(LDFLAGS) $(LIBS) -o $@

$(OBJS): cvs.h cvstypes.h
revcvs.o cvsutils.o rbtree.o: rbtree.h
atom.o nodehash.o revcvs.o revdir.o: hash.h
revdir.o: treepack.c dirpack.c revdir.c
dump.o export.o graph.o main.o collate.o revdir.o: revdir.h

gram.h gram.c: gram.y
	$(BISON)  $(YFLAGS) --defines=gram.h --output-file=gram.c gram.y
lex.h lex.c: lex.l
	$(FLEX) $(LFLAGS) --header-file=lex.h --outfile=lex.c lex.l

gram.o: gram.c lex.h gram.h
import.o: import.c lex.h gram.h
lex.o: lex.c gram.h
	$(CC) $(CPPFLAGS) $(CFLAGS) $(TARGET_ARCH) -Wno-unused-function  -c lex.c

reporting-bugs.html: reporting-bugs.adoc
	asciidoctor reporting-bugs.adoc

man: cvs-fast-export.1 cvssync.1 cvsconvert.1

html: cvs-fast-export.html cvssync.html cvsconvert.html reporting-bugs.html

PROFILE_REPO = ~/software/groff-conversion/groff-mirror/groff
gmon.out: cvs-fast-export
	find $(PROFILE_REPO) -name '*,v' | cvs-fast-export -k -p >/dev/null
PROFILE: gmon.out
	gprof cvs-fast-export >PROFILE

clean:
	rm -f $(OBJS) gram.h gram.c lex.h lex.c cvs-fast-export cvs-fast-export-go
	rm -f *.1 *.html gram.output gmon.out
	rm -f index.html *.tar.gz
	rm -f *.gcno *.gcda

# Validate

# Weird suppressions are required because of strange tricks in Bison and Flex.
CSUPPRESSIONS = -U__UNUSED__ -UYYPARSE_PARAM -UYYTYPE_INT16 -UYYTYPE_INT8 \
	-UYYTYPE_UINT16 -UYYTYPE_UINT8 -UYY_USER_INIT -UYY_READ_BUF_SIZE \
	-UYY_NO_INPUT -UECHO -UYY_START_STACK_INCR -UYY_FATAL_ERROR \
	-U_SC_NPROCESSORS_ONLN -Ushort -Usize_t -Uyytext_ptr \
	-Uyyoverflow -U__cplusplus -U__APPLE__ -DCLOCK_REALTIME=0
cppcheck:
	@cppcheck -I. --quiet --template=gcc --enable=all $(CSUPPRESSIONS) --suppress=checkersReport --suppress=unusedStructMember --suppress=unusedFunction --suppress=unreadVariable --suppress=uselessAssignmentPtrArg --suppress=missingIncludeSystem --suppress=knownConditionTrueFalse --suppress=redundantContinue --suppress=knownPointerToBool --suppress=constVariablePointer --suppress=constParameterPointer $(EXTRA) --inline-suppr *.[ch]

pylint:
	@pylint --score=n cvssync cvsconvert cvsstrip
	@pylint --score=n tests/*.py

# Warning: The regression tests will fail spuriously if your CVS lacks the
# MirOS patches.  These are carried by Debian Linux and derivatives; you can
# check by Looking for "MirDebian" in the output of cvs --version.
check: cvs-fast-export-go cppcheck
	@-shellcheck -f gcc buildprep tests/visualize tests/incremental.sh
	@cd tests >/dev/null; $(MAKE) --quiet CVS_FAST_EXPORT=../cvs-fast-export-go

# Like check, but forces rebuild of the generated test repositories first
cleancheck:
	@$(MAKE) -s -C tests clean
	@$(MAKE) -s check

# Go translation
cvs-fast-export-go:
	go build -o cvs-fast-export-go ./cmd/cvs-fast-export

# Needs clean first because reflowing generated .c and .h files produces ungappy results
reflow: clean
	@clang-format --style="{IndentWidth: 8, UseTab: ForIndentation}" -i $$(find . -name "*.[chl]")

fixme:
	@if command -v rg; then \
		rg --no-heading FIX''ME; \
	else \
		find . -type f -exec grep -n FIX''ME {} /dev/null \; | grep -v "[.]git"; \
	fi

spellcheck:
	@spellcheck local.dic *.adoc

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

# Experimental Go code

cfe: cfe.go
	go build -o cfe cfe.go

rcs-refresh:
	go get github.com/arran4/golang-rcs
