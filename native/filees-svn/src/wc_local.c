#include "filees_svn.h"

#include <stdio.h>
#include <string.h>

#include <apr_hash.h>
#include <apr_strings.h>
#include <svn_dirent_uri.h>
#include <svn_props.h>
#include <svn_string.h>

svn_error_t *filees_wc_add(const char *wc_arg, svn_boolean_t live,
                           const char **rels, int n, apr_pool_t *pool)
{
    const char *wc;
    svn_client_ctx_t *ctx;
    apr_array_header_t *paths;
    int i;
    SVN_ERR(filees_require_wc(&wc, &ctx, wc_arg, live, pool));
    SVN_ERR(filees_abs_paths(&paths, wc, rels, n, pool));
    for (i = 0; i < paths->nelts; ++i) {
        const char *path = APR_ARRAY_IDX(paths, i, const char *);
        /* FileES: --parents --depth empty, never recursive add of a tree. */
        SVN_ERR(svn_client_add5(path, svn_depth_empty, FALSE, FALSE, FALSE, TRUE, ctx, pool));
    }
    return SVN_NO_ERROR;
}

svn_error_t *filees_wc_delete(const char *wc_arg, svn_boolean_t live,
                              const char **rels, int n, apr_pool_t *pool)
{
    const char *wc;
    svn_client_ctx_t *ctx;
    apr_array_header_t *paths;
    SVN_ERR(filees_require_wc(&wc, &ctx, wc_arg, live, pool));
    SVN_ERR(filees_abs_paths(&paths, wc, rels, n, pool));
    return svn_client_delete4(paths, FALSE, FALSE, NULL, NULL, NULL, ctx, pool);
}

struct status_baton {
    const char *wc;
    apr_array_header_t *rows;
    apr_pool_t *pool;
};

struct status_row {
    const char *path;
    const char *item;
    const char *props;
};

static svn_error_t *collect_status(void *baton, const char *path,
                                   const svn_client_status_t *status, apr_pool_t *pool)
{
    struct status_baton *b = baton;
    struct status_row *row;
    const char *rel;
    (void)path;
    (void)pool;
    SVN_ERR(filees_relpath(&rel, b->wc, status->local_abspath, b->pool));
    row = apr_array_push(b->rows);
    row->path = apr_pstrdup(b->pool, rel);
    /* CLI --xml: item is text when the only change is properties
       (node_status=modified, text_status=normal, prop_status=modified). */
    row->item = filees_status_kind(status->node_status == svn_wc_status_modified
                                       ? status->text_status
                                       : status->node_status);
    row->props = filees_status_kind(status->prop_status);
    return SVN_NO_ERROR;
}

svn_error_t *filees_wc_status(const char *wc_arg, svn_boolean_t live,
                              const char **rels, int n, svn_depth_t depth,
                              apr_pool_t *pool)
{
    const char *wc;
    svn_client_ctx_t *ctx;
    svn_opt_revision_t rev;
    struct status_baton b;
    int i, first = 1;
    SVN_ERR(filees_require_wc(&wc, &ctx, wc_arg, live, pool));
    rev.kind = svn_opt_revision_working;
    b.wc = wc;
    b.pool = pool;
    b.rows = apr_array_make(pool, 16, sizeof(struct status_row));
    if (n == 0) {
        SVN_ERR(svn_client_status6(NULL, ctx, wc, &rev, depth,
                                  TRUE, FALSE, FALSE, TRUE, TRUE, TRUE,
                                  NULL, collect_status, &b, pool));
    } else {
        apr_array_header_t *paths;
        SVN_ERR(filees_abs_paths(&paths, wc, rels, n, pool));
        for (i = 0; i < paths->nelts; ++i) {
            const char *path = APR_ARRAY_IDX(paths, i, const char *);
            SVN_ERR(svn_client_status6(NULL, ctx, path, &rev, depth,
                                      TRUE, FALSE, FALSE, TRUE, TRUE, TRUE,
                                      NULL, collect_status, &b, pool));
        }
    }
    printf("{\"schema\":\"" FILEES_SVN_SCHEMA "\",\"ok\":true,\"entries\":[");
    for (i = 0; i < b.rows->nelts; ++i) {
        struct status_row *row = &APR_ARRAY_IDX(b.rows, i, struct status_row);
        if (!first) putchar(',');
        first = 0;
        printf("{\"path\":");
        filees_json_string(row->path);
        printf(",\"item\":");
        filees_json_string(row->item);
        printf(",\"props\":");
        filees_json_string(row->props);
        putchar('}');
    }
    puts("]}");
    return SVN_NO_ERROR;
}

svn_error_t *filees_wc_propset(const char *wc_arg, svn_boolean_t live,
                               const char *name, const char *value,
                               const char **rels, int n, apr_pool_t *pool)
{
    const char *wc;
    svn_client_ctx_t *ctx;
    apr_array_header_t *paths;
    svn_string_t *val;
    SVN_ERR(filees_require_wc(&wc, &ctx, wc_arg, live, pool));
    if (!name || !*name || strchr(name, ':') == NULL)
        return filees_refuse("property name must be a valid SVN property");
    SVN_ERR(filees_abs_paths(&paths, wc, rels, n, pool));
    val = svn_string_create(value ? value : "", pool);
    return svn_client_propset_local(name, val, paths, svn_depth_empty, FALSE, NULL, ctx, pool);
}

svn_error_t *filees_wc_propdel(const char *wc_arg, svn_boolean_t live,
                               const char *name, const char **rels, int n,
                               apr_pool_t *pool)
{
    const char *wc;
    svn_client_ctx_t *ctx;
    apr_array_header_t *paths;
    SVN_ERR(filees_require_wc(&wc, &ctx, wc_arg, live, pool));
    if (!name || !*name) return filees_refuse("property name required");
    SVN_ERR(filees_abs_paths(&paths, wc, rels, n, pool));
    return svn_client_propset_local(name, NULL, paths, svn_depth_empty, FALSE, NULL, ctx, pool);
}

struct prop_row {
    const char *path;
    const char *value;
};

svn_error_t *filees_wc_propget(const char *wc_arg, svn_boolean_t live,
                               const char *name, const char **rels, int n,
                               svn_boolean_t recursive, apr_pool_t *pool)
{
    const char *wc;
    svn_client_ctx_t *ctx;
    svn_opt_revision_t peg, rev;
    svn_depth_t depth = recursive ? svn_depth_infinity : svn_depth_empty;
    apr_array_header_t *rows;
    int i, t, first = 1;
    const char *targets[FILEES_SVN_MAX_PATHS];
    int nt;
    SVN_ERR(filees_require_wc(&wc, &ctx, wc_arg, live, pool));
    if (!name || !*name) return filees_refuse("property name required");
    peg.kind = svn_opt_revision_working;
    rev.kind = svn_opt_revision_working;
    if (n == 0) {
        targets[0] = wc;
        nt = 1;
    } else {
        apr_array_header_t *paths;
        SVN_ERR(filees_abs_paths(&paths, wc, rels, n, pool));
        nt = paths->nelts;
        for (i = 0; i < nt; ++i)
            targets[i] = APR_ARRAY_IDX(paths, i, const char *);
    }
    rows = apr_array_make(pool, 8, sizeof(struct prop_row));
    for (t = 0; t < nt; ++t) {
        apr_hash_t *props;
        apr_hash_index_t *hi;
        SVN_ERR(svn_client_propget5(&props, NULL, name, targets[t], &peg, &rev,
                                    NULL, depth, NULL, ctx, pool, pool));
        for (hi = apr_hash_first(pool, props); hi; hi = apr_hash_next(hi)) {
            const char *abspath, *rel;
            svn_string_t *val;
            struct prop_row *row;
            apr_hash_this(hi, (const void **)&abspath, NULL, (void **)&val);
            SVN_ERR(filees_relpath(&rel, wc, abspath, pool));
            row = apr_array_push(rows);
            row->path = apr_pstrdup(pool, rel);
            row->value = apr_pstrdup(pool, val && val->data ? val->data : "");
        }
    }
    printf("{\"schema\":\"" FILEES_SVN_SCHEMA "\",\"ok\":true,\"targets\":[");
    for (i = 0; i < rows->nelts; ++i) {
        struct prop_row *row = &APR_ARRAY_IDX(rows, i, struct prop_row);
        if (!first) putchar(',');
        first = 0;
        printf("{\"path\":");
        filees_json_string(row->path);
        printf(",\"value\":");
        filees_json_string(row->value);
        putchar('}');
    }
    puts("]}");
    return SVN_NO_ERROR;
}

svn_error_t *filees_wc_cleanup(const char *wc_arg, svn_boolean_t live,
                               apr_pool_t *pool)
{
    const char *wc;
    svn_client_ctx_t *ctx;
    SVN_ERR(filees_require_wc(&wc, &ctx, wc_arg, live, pool));
    return svn_client_cleanup2(wc, TRUE, TRUE, TRUE, FALSE, FALSE, ctx, pool);
}

svn_error_t *filees_wc_revert(const char *wc_arg, svn_boolean_t live,
                              const char **rels, int n, apr_pool_t *pool)
{
    const char *wc;
    svn_client_ctx_t *ctx;
    apr_array_header_t *paths;
    SVN_ERR(filees_require_wc(&wc, &ctx, wc_arg, live, pool));
    SVN_ERR(filees_abs_paths(&paths, wc, rels, n, pool));
    return svn_client_revert4(paths, svn_depth_empty, NULL, FALSE, FALSE, TRUE, ctx, pool);
}

svn_error_t *filees_wc_resolve(const char *wc_arg, svn_boolean_t live,
                               const char **rels, int n,
                               svn_wc_conflict_choice_t choice, apr_pool_t *pool)
{
    const char *wc;
    svn_client_ctx_t *ctx;
    apr_array_header_t *paths;
    int i;
    SVN_ERR(filees_require_wc(&wc, &ctx, wc_arg, live, pool));
    SVN_ERR(filees_abs_paths(&paths, wc, rels, n, pool));
    for (i = 0; i < paths->nelts; ++i) {
        const char *path = APR_ARRAY_IDX(paths, i, const char *);
        SVN_ERR(svn_client_resolve(path, svn_depth_empty, choice, ctx, pool));
    }
    return SVN_NO_ERROR;
}

struct info_row {
    const char *path;
    const char *url;
    const char *repos_root;
    const char *repos_uuid;
    const char *kind;
    svn_revnum_t rev;
    svn_revnum_t last_changed_rev;
};

struct info_baton {
    const char *wc;
    apr_array_header_t *rows;
    apr_pool_t *pool;
};

static svn_error_t *collect_info(void *baton, const char *abspath_or_url,
                                 const svn_client_info2_t *info, apr_pool_t *pool)
{
    struct info_baton *b = baton;
    struct info_row *row;
    const char *rel;
    (void)pool;
    SVN_ERR(filees_relpath(&rel, b->wc, abspath_or_url, b->pool));
    row = apr_array_push(b->rows);
    row->path = apr_pstrdup(b->pool, rel);
    row->url = info->URL ? apr_pstrdup(b->pool, info->URL) : NULL;
    row->repos_root = info->repos_root_URL ? apr_pstrdup(b->pool, info->repos_root_URL) : NULL;
    row->repos_uuid = info->repos_UUID ? apr_pstrdup(b->pool, info->repos_UUID) : NULL;
    row->kind = filees_node_kind(info->kind);
    row->rev = info->rev;
    row->last_changed_rev = info->last_changed_rev;
    return SVN_NO_ERROR;
}

static void info_json_revision(const char *name, svn_revnum_t rev)
{
    /* An invalid revision is reported as null rather than -1. A caller that
     * parses -1 as a number gets a plausible-looking answer to a question the
     * working copy could not answer, which is the shape of bug this codebase
     * keeps paying for elsewhere. */
    printf(",\"%s\":", name);
    if (SVN_IS_VALID_REVNUM(rev)) printf("%ld", (long)rev);
    else printf("null");
}

static void info_json_field(const char *name, const char *value)
{
    printf(",\"%s\":", name);
    if (value) filees_json_string(value);
    else printf("null");
}

/* info is the last WC-local verb from the desktop inventory. It answers only
 * about the working copy: an URL target would be an RA operation and belongs
 * with checkout/update, not here.
 *
 * The emitted fields are the ones the daemon actually consumes -
 * VerifyCommittedMove reads url, repository root and the last-changed revision
 * (pkg/client/native_move.go:133), and Revision() reads one number
 * (client.go:666) - plus uuid and kind, which cost nothing and answer "is this
 * the repository I think it is". */
svn_error_t *filees_wc_info(const char *wc_arg, svn_boolean_t live,
                            const char **rels, int n, apr_pool_t *pool)
{
    const char *wc;
    svn_client_ctx_t *ctx;
    apr_array_header_t *paths;
    struct info_baton b;
    int i, first = 1;

    SVN_ERR(filees_require_wc(&wc, &ctx, wc_arg, live, pool));
    b.wc = wc;
    b.pool = pool;
    b.rows = apr_array_make(pool, 4, sizeof(struct info_row));
    if (n == 0) {
        paths = apr_array_make(pool, 1, sizeof(const char *));
        APR_ARRAY_PUSH(paths, const char *) = wc;
    } else {
        SVN_ERR(filees_abs_paths(&paths, wc, rels, n, pool));
    }
    for (i = 0; i < paths->nelts; ++i) {
        const char *path = APR_ARRAY_IDX(paths, i, const char *);
        /* Both revisions NULL is not a shortcut for a default - it is the
         * only input that keeps svn_client_info4 inside the working copy.
         * libsvn_client/info.c:353 takes the local branch solely when peg and
         * revision are NULL or unspecified; anything else, WORKING and BASE
         * included, opens an RA session. Measured 2026-09-08: passing WORKING
         * made the receiver report repository-relative names and the verb
         * quietly became a network call. */
        SVN_ERR(svn_client_info4(path, NULL, NULL, svn_depth_empty,
                                 FALSE, TRUE, FALSE, NULL,
                                 collect_info, &b, ctx, pool));
    }
    printf("{\"schema\":\"" FILEES_SVN_SCHEMA "\",\"ok\":true,\"entries\":[");
    for (i = 0; i < b.rows->nelts; ++i) {
        struct info_row *row = &APR_ARRAY_IDX(b.rows, i, struct info_row);
        if (!first) putchar(',');
        first = 0;
        printf("{\"path\":");
        filees_json_string(row->path);
        info_json_field("url", row->url);
        info_json_field("repos_root_url", row->repos_root);
        info_json_field("repos_uuid", row->repos_uuid);
        info_json_field("kind", row->kind);
        info_json_revision("revision", row->rev);
        info_json_revision("last_changed_rev", row->last_changed_rev);
        putchar('}');
    }
    puts("]}");
    return SVN_NO_ERROR;
}
