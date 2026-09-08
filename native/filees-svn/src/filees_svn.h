/* Shared FileES native SVN helper. WC-local verbs plus metadata-only move. */
#ifndef FILEES_SVN_H
#define FILEES_SVN_H

#include <apr_file_info.h>
#include <apr_pools.h>
#include <apr_tables.h>
#include <svn_client.h>
#include <svn_error.h>
#include <svn_types.h>
#include <svn_wc.h>

#define FILEES_SVN_MARKER ".filees-native-probe"
#define FILEES_SVN_SCHEMA "filees.native-svn/v1"
/* Per invocation. The Go adapter splits daemon batches (default 1000). */
#define FILEES_SVN_MAX_PATHS 512

svn_error_t *filees_refuse(const char *message);
void filees_json_string(const char *s);
int filees_failure(svn_error_t *err);
int filees_safe_relative(const char *path);
const char *filees_status_kind(enum svn_wc_status_kind kind);
const char *filees_node_kind(svn_node_kind_t kind);

/* Exact WC root. live requires .filees; disposable requires the probe marker. */
svn_error_t *filees_require_wc(const char **wc_abspath, svn_client_ctx_t **ctx,
                               const char *wc_arg, svn_boolean_t live,
                               apr_pool_t *pool);
svn_error_t *filees_plain_node(const char *path, apr_filetype_e wanted,
                               svn_boolean_t missing, apr_pool_t *pool);
svn_error_t *filees_relpath(const char **rel, const char *wc,
                            const char *abspath, apr_pool_t *pool);
svn_error_t *filees_abs_paths(apr_array_header_t **out, const char *wc,
                              const char **rels, int nrels, apr_pool_t *pool);

svn_error_t *filees_record_move(const char *wc_arg, const char *old_rel,
                                const char *new_rel, svn_boolean_t live,
                                const char **state, apr_pool_t *pool);

/* Remote verbs. No working copy, no .filees marker; see ra.c for the guard. */
svn_error_t *filees_ra_ctx(svn_client_ctx_t **ctx, apr_pool_t *pool);
svn_error_t *filees_ra_ctx_auth(svn_client_ctx_t *ctx, apr_pool_t *pool);
svn_error_t *filees_ra_target(const char **canonical, const char *url,
                              apr_pool_t *pool);
svn_error_t *filees_ra_cat(const char *url, const char *out_path,
                           svn_revnum_t revision, apr_pool_t *pool);
svn_error_t *filees_ra_checkout(const char *url, const char *wc,
                                svn_revnum_t revision, svn_boolean_t force,
                                apr_pool_t *pool);
svn_error_t *filees_ra_update(const char *wc, svn_boolean_t live,
                              const char **rels, int n, svn_depth_t depth,
                              svn_revnum_t revision, apr_pool_t *pool);
svn_error_t *filees_ra_commit(const char *wc, svn_boolean_t live,
                              const char **rels, int n, const char *message,
                              svn_boolean_t keep_locks, const char **revprops,
                              int nrevprops, apr_pool_t *pool);
svn_error_t *filees_ra_lock(const char *wc, svn_boolean_t live,
                            const char **rels, int n, const char *comment,
                            apr_pool_t *pool);
svn_error_t *filees_ra_unlock(const char *wc, svn_boolean_t live,
                              const char **rels, int n, apr_pool_t *pool);
svn_error_t *filees_log(svn_client_ctx_t *ctx, const char *target,
                        const svn_opt_revision_t *peg,
                        const svn_opt_revision_t *start,
                        const svn_opt_revision_t *end, int limit,
                        svn_boolean_t changed_paths, const char **revprops,
                        int nrevprops, apr_pool_t *pool);

svn_error_t *filees_wc_add(const char *wc, svn_boolean_t live,
                           const char **rels, int n, apr_pool_t *pool);
svn_error_t *filees_wc_delete(const char *wc, svn_boolean_t live,
                              const char **rels, int n, apr_pool_t *pool);
svn_error_t *filees_wc_status(const char *wc, svn_boolean_t live,
                              const char **rels, int n, svn_depth_t depth,
                              apr_pool_t *pool);
svn_error_t *filees_wc_info(const char *wc, svn_boolean_t live,
                            const char **rels, int n, apr_pool_t *pool);
svn_error_t *filees_wc_propset(const char *wc, svn_boolean_t live,
                               const char *name, const char *value,
                               const char **rels, int n, apr_pool_t *pool);
svn_error_t *filees_wc_propdel(const char *wc, svn_boolean_t live,
                               const char *name, const char **rels, int n,
                               apr_pool_t *pool);
svn_error_t *filees_wc_propget(const char *wc, svn_boolean_t live,
                               const char *name, const char **rels, int n,
                               svn_boolean_t recursive, apr_pool_t *pool);
svn_error_t *filees_wc_cleanup(const char *wc, svn_boolean_t live,
                               apr_pool_t *pool);
svn_error_t *filees_wc_revert(const char *wc, svn_boolean_t live,
                              const char **rels, int n, apr_pool_t *pool);
svn_error_t *filees_wc_resolve(const char *wc, svn_boolean_t live,
                               const char **rels, int n,
                               svn_wc_conflict_choice_t choice, apr_pool_t *pool);

#endif
