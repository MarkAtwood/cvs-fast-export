# Test makefile for cvs-fast-export

PATH := ${CURDIR}/..:${CURDIR}:${PATH}
SHELL = sh	# Do not introduce bashisms!
CVS = cvs
DIFF = diff -u -a
CVS_FAST_EXPORT = ../cvs-fast-export
export CVS_FAST_EXPORT
PYTHON = python3

.PHOMY: CHECK FULLCHECK

# Ordinary tests, use pregenerated repos for speed.
check:
	@$(MAKE) --quiet tap | ./tapview

# Full test with rebuild of generated files
fullcheck:
	@$(MAKE) --quiet clean tap | ./tapview

.SUFFIXES: .tst .repo .testrepo .checkout .dot .fi ,v

.tst.repo:
	$(PYTHON) $<
.repo.checkout:
	$(CVS) -d :local:${CURDIR}/$*.repo -Q checkout module && mv module $*.checkout
.repo.dot:
	find $</module -name '*,v' | $(CVS_FAST_EXPORT) -g >$*.dot
.repo.fi:
	find $</module -name '*,v' | $(CVS_FAST_EXPORT) >$*.dot
.testrepo.checkout:
	$(CVS) -d :local:${CURDIR}/$*.testrepo -Q checkout module && mv module $*.checkout
.testrepo.dot:
	find $</module -name '*,v' | $(CVS_FAST_EXPORT) -g >$*.dot
.testrepo.fi:
	find $</module -name '*,v' | $(CVS_FAST_EXPORT) >$*.fi
,v.dot:
	$(CVS_FAST_EXPORT) -g $< >$*.dot

test: s_regress m_regress r_regress g_regress i_regress t_regress c_regress sporadic #  z_regress

update: s_update m_update r_update g_update i_update t_update # z_update

testlist:
	@grep '^##' *.tst *.py *.sh

# USER may not actually be defined inside CI; in that case we assume root.
neutralize.map:
	@echo "$${USER:-root} = foo <foo> -0500" >neutralize.map

TESTLOADS := $(shell ls -1 *.tst | sed '/.tst/s///')
TESTOPTS = -T -A neutralize.map --reposurgeon
s_update: neutralize.map
	@-for file in $(TESTLOADS); do \
	    echo "Remaking $${file}.chk"; \
	    $(MAKE) --quiet $${file}.repo; \
	    $(CVS_FAST_EXPORT) $(TESTOPTS) $${file}.repo/module >$${file}.chk 2>&1; \
	done;
s_regress: neutralize.map
	@echo "# Dump regressions"
	@-for file in $(TESTLOADS); do \
	    legend=$$( ( grep -a '##' $${file}.tst || echo ' ## (no description)' ) | sed 's/## //' ); \
	    $(MAKE) --quiet $${file}.repo; \
	    $(CVS_FAST_EXPORT) $(TESTOPTS) $${file}.repo/module 2>&1 | tapdiffer "$${file}: $${legend}" $${file}.chk; \
	done
TEST_TARGETS = $(TESTLOADS)

MASTERS := $(shell ls -1 *,v | sed '/,v/s///')
m_update:
	@-for file in $(MASTERS); do \
	    echo "Remaking $${file}.chk"; \
	    $(CVS_FAST_EXPORT) $${file},v >$${file}.chk 2>&1; \
	done;
m_regress:
	@echo "# Master-parsing regressions"
	@-for file in $(MASTERS); do \
	    legend=$$(sed <$${file},v -n -e '/^comment[	 ]*@# \(.*\)@;/s//\1/p'); \
	    $(CVS_FAST_EXPORT) $${file},v 2>&1 | tapdiffer "$${file}: $${legend}" $${file}.chk; \
	done
TEST_TARGETS += $(MASTERS)

INCREMENTAL=twobranch
THRESHOLD=104000
i_update: neutralize.map
	@-for file in $(INCREMENTAL); do \
	    echo "Remaking $${file}.inc-chk"; \
	    $(MAKE) --quiet $${file}.repo; \
	    $(CVS_FAST_EXPORT) -T -A neutralize.map -i $(THRESHOLD) $${file}.repo/module >$${file}.inc-chk 2>&1; \
	done;
i_regress: neutralize.map
	@echo "# Incremental-dump regressions"
	@-for file in $(INCREMENTAL); do \
	    legend=$$( ( grep -a '##' $${file}.tst  || echo ' ## (no description)') | sed 's/## //' ); \
	    $(MAKE) --quiet $${file}.repo; \
	    $(CVS_FAST_EXPORT) -T -i $(THRESHOLD) -A neutralize.map $${file}.repo/module 2>&1 | tapdiffer "$${file} $${legend}" $${file}.inc-chk; \
	done
TEST_TARGETS += $(INCREMENTAL)

REDUCED=oldhead issue-57
r_update: neutralize.map
	@-for file in $(REDUCED); do \
	    echo "Remaking $${file}.chk"; \
	    $(CVS_FAST_EXPORT) $(TESTOPTS) $${file}.testrepo/module >$${file}.chk 2>&1; \
	done;
r_regress: neutralize.map
	@echo "# Repo regressions"
	@-for repo in $(REDUCED); do \
	    legend=$$( ( grep -a '##' $${repo}.testrepo/README  || echo ' ## (no description)') | sed 's/## //' ); \
	    $(CVS_FAST_EXPORT) $(TESTOPTS) $${repo}.testrepo/module 2>&1 | tapdiffer "$${repo}: $${legend}" $${repo}.chk; \
	done
TEST_TARGETS += $(REDUCED)

GRAPHTESTS=basic branchy
g_update: neutralize.map
	@-for file in $(GRAPHTESTS); do \
	    echo "Remaking $${file}.dot"; \
	    $(CVS_FAST_EXPORT) -g $(TESTOPTS) $${file}.repo/module >$${file}.dot 2>&1; \
	done;
g_regress: neutralize.map
	@echo "# Graph regressions"
	@-for repo in $(GRAPHTESTS); do \
	     $(CVS_FAST_EXPORT) -g $(TESTOPTS) $${repo}.repo/module 2>&1 | tapdiffer "$${repo}: DOT regression test" $${repo}.dot; \
	done
TEST_TARGETS += $(GRAPHTESTS)

PYTESTS=t9601 t9602 t9603 t9604 t9605 t9606
PATHSTRIP = sed -e '/\/.*tests/s//tests/'
t_regress:
	@echo "# Pathological cases"
	@for pytest in $(PYTESTS); do \
		legend=$$( ( grep -a '##' $${pytest}.py  || echo ' ## (no description)') | sed 's/## //' ); \
		$(PYTHON) $${pytest}.py 2>&1 | $(PATHSTRIP) | tapdiffer "$${pytest}: $${legend}" $${pytest}.err; \
	done
t_update:
	@for pytest in $(PYTESTS); do \
		echo "Remaking $${pytest}.err "; \
		$(PYTHON) $${pytest}.py 2>&1 | $(PATHSTRIP) >$${pytest}.err; \
	done
TEST_TARGETS += $(PYTESTS)

# Omitted:
# branchy.repo - because of illegal tag
# twotag.repo - because of inconsistent tagging
# QED.testrepo - produces a manifest mismatch
# t9601.testrepo - content mismatch expected
# t9602.testrepo - manifest mismatch expected
CT = at.repo basic.repo daughterbranch.repo exec.repo \
	hack1.repo hack2.repo hack3.repo longrev.repo postbranch.repo \
	tagbug.repo twobranch.repo
CD =  oldhead.testrepo t9603.testrepo t9604.testrepo t9605.testrepo \
	vendor.testrepo
c_regress:
	@echo "# Conversion checks"
	@for ct in $(CT); do $(MAKE) --quiet $$ct; cvsconvert -q -n -t $$ct; done
	@for cd in $(CD); do cvsconvert -q -n -t $$cd; done
TEST_TARGETS += $(CD) $(CT)

SPORADIC = incremental.sh
sporadic:
	@echo "# Sporadic tests"
	@for x in $(SPORADIC); do sh $${x}; done
TEST_TARGETS += $(SPORADIC)

# These tests can't be part of normal regressions because we don't seem
# to be able to control when a stripped master will get a new modification
# timestamp.
UNSTRIPPED = $(shell ls issue22.txt,v t9601.testrepo/module/* at.repo/module/*)
z_regress:
	@echo "# Reductions"
	@for rtest in $(UNSTRIPPED); do \
		base=`basename $${rtest}`; \
		../cvsstrip <$${rtest} | tapdiffer "cvsstrip test of $${rtest}" reductions/$${base}.reduced -; \
	done
z_update:
	@for rtest in $(UNSTRIPPED); do \
		base=`basename $${rtest}`; \
		echo "Remaking $${base}.reduced "; \
		../cvsstrip <$${rtest} >reductions/$${base}.reduced; \
	done
#TEST_TARGETS += $(UNSTRIPPED)

clean:
	rm -fr neutralize.map *.checkout *.repo *.pyc *.git *.git.fi __pycache__

# Use this to scope out problems with test counting. 
detailcount:
	@echo "Dump regressions: $(words $(TESTLOADS))"
	@echo "Master-parsing regressions: $(words $(MASTERS))"
	@echo "Incremental-dump regressions: $(words $(INCREMENTAL))"
	@echo "Repo regressions: $(words $(REDUCED))"
	@echo "Graph regressions: $(words $(GRAPHTEST))"
	@echo "Pathological cases: $(words $(PYTESTS))"
	@echo "Conversion checks: $(words $(CD) $(CT))"
	@echo "Sporadic tests: $(words $(SPORADIC))"
	#@echo "Reduction tests: $(words $(UNSTRIPPED))"
	@echo "Total tests: $(words $(TEST_TARGETS))"


# Issue a straight TAP report, no parallelism or filtering
tap: count test
	@echo "# Tests complete"
count:
	@echo 1..$(words $(TEST_TARGETS))
