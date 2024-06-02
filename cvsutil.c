/*
 *  SPDX-FileCopyrightText: Copyright © 2006 Keith Packard <keithp@keithp.com>
 *
 *  SPDX-License-Identifier: GPL-2.0+
 */

#include <assert.h>

#include "cvs.h"

#ifdef REDBLACK
#include "rbtree.h"
#endif /* REDBLACK */

/* discard all master symbols from this CVS context */
static void cvs_symbol_free(cvs_symbol *symbol) {
	cvs_symbol *s;

	while ((s = symbol)) {
		symbol = s->next;
		free(s);
	}
}

/* discard all master branches from this CVS context */
static void cvs_branch_free(cvs_branch *branch) {
	cvs_branch *b;

	while ((b = branch)) {
		branch = b->next;
		free(b);
	}
}

/* discard all master versions from this CVS context */
static void cvs_version_free(cvs_version *version) {
	cvs_version *v;

	while ((v = version)) {
		version = v->next;
		cvs_branch_free(v->branches);
		free(v);
	}
}

/* discard all master patches from this CVS context */
static void cvs_patch_free(cvs_patch *patch) {
	cvs_patch *v;

	while ((v = patch)) {
		patch = v->next;
		free(v);
	}
}

void generator_free(generator_t *gen) {
	cvs_version_free(gen->versions);
	cvs_patch_free(gen->patches);
	clean_hash(&gen->nodehash);
}

/* discard a file object and its storage */
void cvs_file_free(cvs_file *cvs) {
	cvs_symbol_free(cvs->symbols);
#ifdef REDBLACK
	rbtree_free(cvs->symbols_by_name);
#endif /* REDBLACK */
	free(cvs);
}

/* Local Variables:    */
/* mode: c             */
/* c-basic-offset: 8   */
/* indent-tabs-mode: t */
/* End:                */
