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
    row->item = filees_status_kind(status->node_status);
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

svn_error_t *filees_wc_propget(const char *wc_arg, svn_boolean_t live,
                               const char *name, const char **rels, int n,
                               svn_boolean_t recursive, apr_pool_t *pool)
{
    const char *wc;
    svn_client_ctx_t *ctx;
    svn_opt_revision_t peg, rev;
    svn_depth_t depth = recursive ? svn_depth_infinity : svn_depth_empty;
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
    printf("{\"schema\":\"" FILEES_SVN_SCHEMA "\",\"ok\":true,\"targets\":[");
    for (t = 0; t < nt; ++t) {
        apr_hash_t *props;
        apr_hash_index_t *hi;
        SVN_ERR(svn_client_propget5(&props, NULL, name, targets[t], &peg, &rev,
                                    NULL, depth, NULL, ctx, pool, pool));
        for (hi = apr_hash_first(pool, props); hi; hi = apr_hash_next(hi)) {
            const char *abspath, *rel;
            svn_string_t *val;
            apr_hash_this(hi, (const void **)&abspath, NULL, (void **)&val);
            SVN_ERR(filees_relpath(&rel, wc, abspath, pool));
            if (!first) putchar(',');
            first = 0;
            printf("{\"path\":");
            filees_json_string(rel);
            printf(",\"value\":");
            filees_json_string(val ? val->data : "");
            putchar('}');
        }
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
