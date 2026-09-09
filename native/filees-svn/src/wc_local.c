#include "filees_svn.h"

#include <stdio.h>
#include <string.h>

#include <apr_hash.h>
#include <apr_strings.h>
#include <svn_dirent_uri.h>
#include <svn_props.h>
#include <svn_string.h>
#include <svn_time.h>

svn_error_t *filees_wc_add(const char *wc_arg, svn_boolean_t live,
                           const char **rels, int n, apr_pool_t *pool)
{
    const char *wc;
    svn_client_ctx_t *ctx;
    apr_array_header_t *paths;
    int i;
    SVN_ERR(filees_require_wc(&wc, &ctx, wc_arg, live, pool));
    SVN_ERR(filees_abs_paths(&paths, wc, rels, n, pool));
    SVN_ERR(filees_writer_guard(wc, pool));
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
    SVN_ERR(filees_writer_guard(wc, pool));
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
    svn_lock_t *local_lock;
    svn_lock_t *repos_lock;
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
    row->local_lock = status->lock ? svn_lock_dup(status->lock, b->pool) : NULL;
    row->repos_lock = status->repos_lock ? svn_lock_dup(status->repos_lock, b->pool) : NULL;
    return SVN_NO_ERROR;
}

/* status6 cannot open a WC database at an unversioned parent. Only convert
 * that specific failure after proving an existing plain target and an SVN
 * observation of its unversioned ancestor. Missing/obstructed/foreign paths
 * and arbitrary metadata failures remain errors; this never schedules add. */
static svn_error_t *nested_unversioned_status(svn_error_t *original,
                                              const char *path,
                                              svn_client_ctx_t *ctx,
                                              struct status_baton *b,
                                              apr_pool_t *pool)
{
    apr_finfo_t info;
    const char *parent;
    svn_error_t *err;
    svn_opt_revision_t rev;
    if (!svn_error_find_cause(original, SVN_ERR_WC_PATH_NOT_FOUND)) return original;
    if (apr_stat(&info, path, APR_FINFO_TYPE | APR_FINFO_LINK, pool) ||
        (info.filetype != APR_REG && info.filetype != APR_DIR)) return original;
    err = filees_plain_node(path, info.filetype, FALSE, pool);
    if (err) { svn_error_clear(err); return original; }
    rev.kind = svn_opt_revision_working;
    for (parent = svn_dirent_dirname(path, pool); strcmp(parent, b->wc);
         parent = svn_dirent_dirname(parent, pool)) {
        struct status_baton probe;
        if (!svn_dirent_skip_ancestor(b->wc, parent)) return original;
        probe.wc = b->wc;
        probe.pool = pool;
        probe.rows = apr_array_make(pool, 1, sizeof(struct status_row));
        err = svn_client_status6(NULL, ctx, parent, &rev, svn_depth_empty,
                                 TRUE, FALSE, FALSE, TRUE, TRUE, TRUE,
                                 NULL, collect_status, &probe, pool);
        if (err) {
            svn_boolean_t absent = svn_error_find_cause(err, SVN_ERR_WC_PATH_NOT_FOUND) != NULL;
            svn_error_clear(err);
            if (absent) continue;
            return original;
        }
        if (probe.rows->nelts == 1) {
            const struct status_row *ancestor = &APR_ARRAY_IDX(probe.rows, 0, struct status_row);
            if (!strcmp(ancestor->item, "unversioned") || !strcmp(ancestor->item, "ignored")) {
                const char *rel;
                struct status_row *row;
                err = filees_relpath(&rel, b->wc, path, pool);
                if (err) return svn_error_compose_create(original, err);
                row = apr_array_push(b->rows);
                row->path = rel;
                row->item = ancestor->item;
                row->props = "none";
                row->local_lock = row->repos_lock = NULL;
                svn_error_clear(original);
                return SVN_NO_ERROR;
            }
        }
        return original;
    }
    return original;
}

static void status_lock(const char *name, const svn_lock_t *lock, apr_pool_t *pool)
{
    printf(",\"%s\":", name);
    if (!lock) { printf("null"); return; }
    printf("{\"token\":"); filees_json_string(lock->token);
    printf(",\"owner\":"); filees_json_string(lock->owner);
    printf(",\"comment\":"); filees_json_string(lock->comment ? lock->comment : "");
    printf(",\"created\":"); filees_json_string(svn_time_to_cstring(lock->creation_date, pool));
    putchar('}');
}

svn_error_t *filees_wc_status(const char *wc_arg, svn_boolean_t live,
                              const char **rels, int n, svn_depth_t depth,
                              svn_boolean_t remote, svn_boolean_t inspect, apr_pool_t *pool)
{
    const char *wc;
    svn_client_ctx_t *ctx;
    svn_opt_revision_t rev;
    struct status_baton b;
    svn_revnum_t against = SVN_INVALID_REVNUM;
    int i, first = 1;
    if (remote && n > 1) return filees_refuse("remote status requires at most one target");
    if (inspect) {
        if (remote) return filees_refuse("inspection status is offline only");
        SVN_ERR(filees_inspect_wc(&wc, &ctx, wc_arg, pool));
    } else {
        SVN_ERR(filees_require_wc(&wc, &ctx, wc_arg, live, pool));
    }
    if (remote) SVN_ERR(filees_ra_ctx_auth(ctx, pool));
    rev.kind = svn_opt_revision_working;
    b.wc = wc;
    b.pool = pool;
    b.rows = apr_array_make(pool, 16, sizeof(struct status_row));
    if (n == 0) {
        SVN_ERR(svn_client_status6(&against, ctx, wc, &rev, depth,
                                  TRUE, remote, TRUE, TRUE, TRUE, FALSE,
                                  NULL, collect_status, &b, pool));
    } else {
        apr_array_header_t *paths;
        SVN_ERR(filees_abs_paths(&paths, wc, rels, n, pool));
        for (i = 0; i < paths->nelts; ++i) {
            const char *path = APR_ARRAY_IDX(paths, i, const char *);
            int before = b.rows->nelts;
            svn_error_t *err = svn_client_status6(&against, ctx, path, &rev, depth,
                                                  TRUE, remote, TRUE, TRUE, TRUE, FALSE,
                                                  NULL, collect_status, &b, pool);
            if (err) {
                b.rows->nelts = before;
                if (remote) return err; /* no invented repository lock observation */
                SVN_ERR(nested_unversioned_status(err, path, ctx, &b, pool));
            }
        }
    }
    printf("{\"schema\":\"" FILEES_SVN_SCHEMA "\",\"ok\":true,\"remote\":%s,\"entries\":[", remote ? "true" : "false");
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
        if (remote) {
            status_lock("local_lock", row->local_lock, pool);
            status_lock("repos_lock", row->repos_lock, pool);
        }
        putchar('}');
    }
    printf("]");
    if (remote) {
        printf(",\"against_revision\":");
        if (SVN_IS_VALID_REVNUM(against)) printf("%ld", (long)against);
        else printf("null");
    }
    puts("}");
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
    SVN_ERR(filees_writer_guard(wc, pool));
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
    SVN_ERR(filees_writer_guard(wc, pool));
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
    SVN_ERR(filees_writer_guard(wc, pool));
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
    SVN_ERR(filees_writer_guard(wc, pool));
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
    SVN_ERR(filees_writer_guard(wc, pool));
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
    if (b->wc) SVN_ERR(filees_relpath(&rel, b->wc, abspath_or_url, b->pool));
    else rel = abspath_or_url;
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

/* One info receipt for offline WC inspection and explicit URL HEAD. The latter
 * uses the same noninteractive RA context as checkout/update. Unmanaged WC
 * inspection proves identity before adoption, without creating .filees; it
 * does not authorize mutation. Unknown revisions remain null in the receipt. */
svn_error_t *filees_info(const char *url_arg, const char *wc_arg, svn_boolean_t live,
                            svn_boolean_t inspect,
                            const char **rels, int n, apr_pool_t *pool)
{
    const char *wc = NULL, *url = NULL;
    svn_client_ctx_t *ctx;
    apr_array_header_t *paths;
    struct info_baton b;
    int i, first = 1;

    if (url_arg) {
        if (wc_arg || n || inspect) return filees_refuse("info URL cannot have WC options");
        SVN_ERR(filees_ra_target(&url, url_arg, pool));
        SVN_ERR(filees_ra_ctx(&ctx, pool));
    } else if (inspect) SVN_ERR(filees_inspect_wc(&wc, &ctx, wc_arg, pool));
    else SVN_ERR(filees_require_wc(&wc, &ctx, wc_arg, live, pool));
    b.wc = wc;
    b.pool = pool;
    b.rows = apr_array_make(pool, 4, sizeof(struct info_row));
    if (n == 0) {
        paths = apr_array_make(pool, 1, sizeof(const char *));
        APR_ARRAY_PUSH(paths, const char *) = url ? url : wc;
    } else {
        SVN_ERR(filees_abs_paths(&paths, wc, rels, n, pool));
    }
    for (i = 0; i < paths->nelts; ++i) {
        const char *path = APR_ARRAY_IDX(paths, i, const char *);
        svn_opt_revision_t head;
        head.kind = svn_opt_revision_head;
        /* Both revisions NULL is not a shortcut for a default - it is the
         * only input that keeps svn_client_info4 inside the working copy.
         * libsvn_client/info.c:353 takes the local branch solely when peg and
         * revision are NULL or unspecified; anything else, WORKING and BASE
         * included, opens an RA session. Measured 2026-09-08: passing WORKING
         * made the receiver report repository-relative names and the verb
         * quietly became a network call. */
        SVN_ERR(svn_client_info4(path, url ? &head : NULL, url ? &head : NULL, svn_depth_empty,
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
