/* Remote verbs. Everything here talks to a server; nothing here needs a
 * working copy, which makes it the first part of the helper outside the
 * .filees marker guard - see filees_ra_target for what replaces it. */
#include "filees_svn.h"

#include <stdio.h>
#include <string.h>

#include <apr_file_io.h>
#include <apr_strings.h>
#include <svn_cmdline.h>
#include <svn_config.h>
#include <svn_dirent_uri.h>
#include <svn_io.h>
#include <svn_hash.h>
#include <svn_path.h>
#include <svn_props.h>
#include <svn_string.h>

/* filees_ra_ctx builds a context able to reach a server.
 *
 * The config hash stays NULL, exactly as the WC-local verbs leave it. That is
 * not an oversight: measured 2026-09-08 against the production server
 * (reports/NATIVE_SVN_RA_SSH_PROBE_2026-09-08.md), an svn+ssh tunnel opens the
 * same with a NULL config as with the user's, because the built-in [tunnels]
 * ssh entry honours SVN_SSH either way. NULL is the more hermetic of the two:
 * nothing the machine happens to have in ~/.subversion can change how a pinned
 * deployment connects.
 *
 * The auth baton is non-interactive and caches nothing. FileES authenticates
 * with the ssh key the daemon pins; a prompt here would be a hang, and a cache
 * would be credentials this process has no business keeping. */
svn_error_t *filees_ra_ctx_auth(svn_client_ctx_t *ctx, apr_pool_t *pool)
{
    SVN_ERR(svn_cmdline_create_auth_baton2(&ctx->auth_baton,
                                           TRUE,  /* non_interactive */
                                           NULL, NULL, NULL,
                                           TRUE,  /* no_auth_cache */
                                           FALSE, FALSE, FALSE, FALSE, FALSE,
                                           NULL, NULL, NULL, pool));
    return SVN_NO_ERROR;
}

svn_error_t *filees_ra_ctx(svn_client_ctx_t **ctx, apr_pool_t *pool)
{
    SVN_ERR(svn_client_create_context2(ctx, NULL, pool));
    return filees_ra_ctx_auth(*ctx, pool);
}

/* filees_ra_target validates a repository URL.
 *
 * The WC verbs are guarded by a marker file inside the directory they are about
 * to touch. A remote verb has no such anchor, so the guard here is narrower and
 * different in kind: the argument must be a URL and nothing else. A local path
 * arriving where a URL is expected means the caller is confused, and the one
 * thing this process must never do is start reading the filesystem because an
 * argument was mistyped. */
svn_error_t *filees_ra_target(const char **canonical, const char *url,
                                     apr_pool_t *pool)
{
    if (!url || !*url) return filees_refuse("missing repository URL");
    if (!svn_path_is_url(url)) return filees_refuse("expected a repository URL");
    return svn_uri_canonicalize_safe(canonical, NULL, url, pool, pool);
}

/* filees_ra_cat writes one repository file to disk.
 *
 * Keywords are deliberately NOT expanded. The CLI expands them by default, and
 * expansion substitutes the URL and revision into the bytes - so the same
 * committed file yields different content depending on where it was fetched
 * from. This verb exists to carry release material that is verified by
 * signature, and a signature over context-dependent bytes verifies nothing.
 *
 * The download lands on a ".part" sibling opened exclusively and is renamed
 * only after the stream closes. A truncated fetch therefore never occupies the
 * final name: an interrupted self-update leaves nothing that looks finished. */
svn_error_t *filees_ra_cat(const char *url_arg, const char *out_path,
                           svn_revnum_t revision, apr_pool_t *pool)
{
    const char *url, *partial;
    svn_client_ctx_t *ctx;
    svn_opt_revision_t peg, rev;
    apr_file_t *file;
    svn_stream_t *stream;
    apr_finfo_t finfo;
    apr_status_t status;

    SVN_ERR(filees_ra_target(&url, url_arg, pool));
    if (!out_path || !*out_path) return filees_refuse("--out must be an absolute path");
    /* SVN dirents are internal style, so a Windows path arrives looking
     * relative until its separators are converted. filees_require_wc does the
     * same to its argument; skipping it here made every absolute Windows
     * target refuse itself. */
    out_path = svn_dirent_internal_style(out_path, pool);
    if (!svn_dirent_is_absolute(out_path))
        return filees_refuse("--out must be an absolute path");
    {
        apr_finfo_t existing;
        if (apr_stat(&existing, out_path, APR_FINFO_TYPE, pool) == APR_SUCCESS)
            return filees_refuse("--out already exists; this verb never overwrites");
    }
    /* Also refuses a symlink anywhere in the parent chain. */
    SVN_ERR(filees_plain_node(out_path, APR_REG, TRUE, pool));

    peg.kind = svn_opt_revision_unspecified;
    if (SVN_IS_VALID_REVNUM(revision)) {
        rev.kind = svn_opt_revision_number;
        rev.value.number = revision;
    } else {
        rev.kind = svn_opt_revision_head;
    }

    partial = apr_pstrcat(pool, out_path, ".part", NULL);
    status = apr_file_open(&file, partial,
                           APR_FOPEN_CREATE | APR_FOPEN_WRITE | APR_FOPEN_EXCL,
                           APR_FPROT_UREAD | APR_FPROT_UWRITE, pool);
    if (status != APR_SUCCESS)
        return svn_error_wrap_apr(status, "cannot create %s", partial);

    stream = svn_stream_from_aprfile2(file, FALSE, pool);
    SVN_ERR(filees_ra_ctx(&ctx, pool));
    {
        /* A failed fetch must leave nothing behind. Without this the .part
         * survives and the next attempt fails on the exclusive open instead
         * of on the real reason - a retry that reports the wrong problem. */
        svn_error_t *err = svn_client_cat3(NULL, stream, url, &peg, &rev, FALSE,
                                           ctx, pool, pool);
        if (!err) err = svn_stream_close(stream);
        else svn_error_clear(svn_stream_close(stream));
        if (err) {
            svn_error_clear(svn_io_remove_file2(partial, TRUE, pool));
            return err;
        }
    }

    if ((status = apr_stat(&finfo, partial, APR_FINFO_SIZE, pool)) != APR_SUCCESS)
        return svn_error_wrap_apr(status, "cannot measure %s", partial);
    SVN_ERR(svn_io_file_rename2(partial, out_path, FALSE, pool));

    printf("{\"schema\":\"" FILEES_SVN_SCHEMA "\",\"ok\":true,\"bytes\":%" APR_OFF_T_FMT,
           finfo.size);
    if (SVN_IS_VALID_REVNUM(revision)) printf(",\"revision\":%ld", (long)revision);
    puts("}");
    return SVN_NO_ERROR;
}

struct log_path {
    const char *path;
    const char *copyfrom_path;
    svn_revnum_t copyfrom_rev;
    char action;
    const char *kind;
};

struct log_entry {
    svn_revnum_t revision;
    const char *author;
    const char *date;
    const char *message;
    apr_array_header_t *paths;  /* struct log_path */
    apr_array_header_t *extra;  /* struct log_revprop */
};

struct log_revprop {
    const char *name;
    const char *value;
};

struct log_baton {
    apr_array_header_t *entries; /* struct log_entry */
    apr_pool_t *pool;
};

/* Hoisted into named fields; anything else the caller asked for goes to
 * revprops, because a caller that named a property wants it back under the
 * name it asked with. */
static svn_boolean_t log_standard_revprop(const char *name)
{
    return !strcmp(name, SVN_PROP_REVISION_AUTHOR)
        || !strcmp(name, SVN_PROP_REVISION_DATE)
        || !strcmp(name, SVN_PROP_REVISION_LOG);
}

/* The copy is not decoration. svn_log_entry_t and its revprops live in the
 * receiver's scratch pool, which is cleared between entries, so a pointer kept
 * from one entry is reading freed memory by the next. Measured 2026-09-08: the
 * newest entry came back with an author of "08T15:35:05.016123Z" - the tail of
 * some other entry's date - and a date of raw bytes. */
static const char *log_revprop(apr_hash_t *props, const char *name,
                               apr_pool_t *pool)
{
    svn_string_t *value = props ? svn_hash_gets(props, name) : NULL;
    return value ? apr_pstrmemdup(pool, value->data, value->len) : NULL;
}

static svn_error_t *collect_log(void *baton, svn_log_entry_t *entry,
                                apr_pool_t *pool)
{
    struct log_baton *b = baton;
    struct log_entry *row;
    (void)pool;

    /* A revision of SVN_INVALID_REVNUM closes a merged-revision group. It
     * carries no commit and must not become an entry. */
    if (!SVN_IS_VALID_REVNUM(entry->revision)) return SVN_NO_ERROR;

    row = apr_array_push(b->entries);
    row->revision = entry->revision;
    row->author = log_revprop(entry->revprops, SVN_PROP_REVISION_AUTHOR, b->pool);
    row->date = log_revprop(entry->revprops, SVN_PROP_REVISION_DATE, b->pool);
    row->message = log_revprop(entry->revprops, SVN_PROP_REVISION_LOG, b->pool);
    row->paths = apr_array_make(b->pool, 4, sizeof(struct log_path));
    row->extra = apr_array_make(b->pool, 2, sizeof(struct log_revprop));

    if (entry->revprops) {
        apr_hash_index_t *hi;
        for (hi = apr_hash_first(b->pool, entry->revprops); hi; hi = apr_hash_next(hi)) {
            const char *name = apr_hash_this_key(hi);
            const svn_string_t *value = apr_hash_this_val(hi);
            struct log_revprop *prop;
            if (log_standard_revprop(name)) continue;
            prop = apr_array_push(row->extra);
            prop->name = apr_pstrdup(b->pool, name);
            prop->value = value ? apr_pstrdup(b->pool, value->data) : NULL;
        }
    }
    if (entry->changed_paths2) {
        apr_hash_index_t *hi;
        for (hi = apr_hash_first(b->pool, entry->changed_paths2); hi; hi = apr_hash_next(hi)) {
            const char *path = apr_hash_this_key(hi);
            const svn_log_changed_path2_t *change = apr_hash_this_val(hi);
            struct log_path *row_path = apr_array_push(row->paths);
            row_path->path = apr_pstrdup(b->pool, path);
            row_path->action = change->action;
            row_path->copyfrom_path = change->copyfrom_path
                                          ? apr_pstrdup(b->pool, change->copyfrom_path)
                                          : NULL;
            row_path->copyfrom_rev = change->copyfrom_rev;
            row_path->kind = filees_node_kind(change->node_kind);
        }
    }
    return SVN_NO_ERROR;
}

static void log_json_field(const char *name, const char *value)
{
    printf(",\"%s\":", name);
    if (value) filees_json_string(value);
    else printf("null");
}

/* filees_log reads history for one target.
 *
 * It serves three callers that used to be three CLI invocations: the shout
 * inbox (revision and message), the commit receipt lookup (a named revprop,
 * hence --revprop), and move-result recovery (changed paths with copyfrom,
 * hence --changed-paths). One verb, because they are one question asked with
 * different fields.
 *
 * Entries are collected before anything is printed. Streaming them would be
 * cheaper, but a failure halfway would already have emitted an opening brace,
 * and this protocol promises one JSON document per invocation - a truncated
 * one is worse than a late one. Callers bound the size with --limit. */
svn_error_t *filees_log(svn_client_ctx_t *ctx, const char *target,
                        const svn_opt_revision_t *peg,
                        const svn_opt_revision_t *start,
                        const svn_opt_revision_t *end, int limit,
                        svn_boolean_t changed_paths, const char **revprops,
                        int nrevprops, apr_pool_t *pool)
{
    apr_array_header_t *targets, *ranges, *props;
    svn_opt_revision_range_t range;
    struct log_baton b;
    int i, j, first = 1;

    targets = apr_array_make(pool, 1, sizeof(const char *));
    APR_ARRAY_PUSH(targets, const char *) = target;

    range.start = *start;
    range.end = *end;
    ranges = apr_array_make(pool, 1, sizeof(svn_opt_revision_range_t *));
    APR_ARRAY_PUSH(ranges, svn_opt_revision_range_t *) = &range;

    /* The three standard properties are always requested, plus whatever the
     * caller named. Passing NULL would fetch every revprop the repository
     * holds, which is somebody else's data and none of our business. */
    props = apr_array_make(pool, 3 + nrevprops, sizeof(const char *));
    APR_ARRAY_PUSH(props, const char *) = SVN_PROP_REVISION_AUTHOR;
    APR_ARRAY_PUSH(props, const char *) = SVN_PROP_REVISION_DATE;
    APR_ARRAY_PUSH(props, const char *) = SVN_PROP_REVISION_LOG;
    for (i = 0; i < nrevprops; ++i)
        APR_ARRAY_PUSH(props, const char *) = revprops[i];

    b.entries = apr_array_make(pool, 16, sizeof(struct log_entry));
    b.pool = pool;
    SVN_ERR(svn_client_log5(targets, peg, ranges, limit, changed_paths,
                            TRUE /* strict_node_history */,
                            FALSE /* include_merged_revisions */,
                            props, collect_log, &b, ctx, pool));

    printf("{\"schema\":\"" FILEES_SVN_SCHEMA "\",\"ok\":true,\"entries\":[");
    for (i = 0; i < b.entries->nelts; ++i) {
        struct log_entry *row = &APR_ARRAY_IDX(b.entries, i, struct log_entry);
        if (!first) putchar(',');
        first = 0;
        printf("{\"revision\":%ld", (long)row->revision);
        log_json_field("author", row->author);
        log_json_field("date", row->date);
        log_json_field("message", row->message);
        printf(",\"revprops\":{");
        for (j = 0; j < row->extra->nelts; ++j) {
            struct log_revprop *prop = &APR_ARRAY_IDX(row->extra, j, struct log_revprop);
            if (j) putchar(',');
            filees_json_string(prop->name);
            putchar(':');
            if (prop->value) filees_json_string(prop->value);
            else printf("null");
        }
        printf("},\"paths\":[");
        for (j = 0; j < row->paths->nelts; ++j) {
            struct log_path *p = &APR_ARRAY_IDX(row->paths, j, struct log_path);
            char action[2];
            if (j) putchar(',');
            action[0] = p->action;
            action[1] = '\0';
            printf("{\"path\":");
            filees_json_string(p->path);
            log_json_field("action", action);
            log_json_field("kind", p->kind);
            log_json_field("copyfrom_path", p->copyfrom_path);
            printf(",\"copyfrom_rev\":");
            if (SVN_IS_VALID_REVNUM(p->copyfrom_rev)) printf("%ld", (long)p->copyfrom_rev);
            else printf("null");
            putchar('}');
        }
        printf("]}");
    }
    puts("]}");
    return SVN_NO_ERROR;
}

struct incoming_change { const char *path; char action; };
struct notify_baton {
    const char *wc;
    apr_array_header_t *conflicts; /* const char * */
    apr_array_header_t *changes; /* struct incoming_change */
    apr_pool_t *pool;
};

/* Conflicts are taken from notifications, not from parsed output.
 *
 * The daemon reads them today by scanning the CLI's printed lines
 * (pkg/commit/reconcile.go, parseConflicts). Handing back a structured list
 * removes that parser rather than moving it: a translated or reworded
 * Subversion is then a non-event instead of a silent loss of every conflict.
 *
 * The path is copied. svn_wc_notify_t lives in a pool the caller clears
 * between notifications - the same trap that gave log an author built from the
 * tail of another entry's date. */
static void collect_notify(void *baton, const svn_wc_notify_t *notify,
                           apr_pool_t *pool)
{
    struct notify_baton *b = baton;
    const char *path;
    svn_boolean_t conflicted;
    (void)pool;

    conflicted = notify->content_state == svn_wc_notify_state_conflicted
                 || notify->prop_state == svn_wc_notify_state_conflicted
                 || notify->action == svn_wc_notify_tree_conflict;
    if (!notify->path) return;

    path = notify->path;
    if (b->wc && svn_dirent_is_absolute(path)) {
        const char *rel = svn_dirent_skip_ancestor(b->wc, path);
        if (rel && *rel) path = rel;
    }
    if (conflicted) {
        /* Never hide a root/unsupported conflict. Go refuses an unrepresentable
         * conflict receipt instead of treating that update as clean. */
        APR_ARRAY_PUSH(b->conflicts, const char *) = apr_pstrdup(b->pool, path);
    } else {
        char action = 0;
        if (!filees_safe_relative(path)) return; /* only data gets an incoming receipt */
        if (notify->content_state == svn_wc_notify_state_merged ||
            notify->prop_state == svn_wc_notify_state_merged) return;
        if (notify->action == svn_wc_notify_update_add) action = 'A';
        else if (notify->action == svn_wc_notify_update_delete) action = 'D';
        else if (notify->action == svn_wc_notify_update_update &&
                 notify->content_state == svn_wc_notify_state_changed &&
                 notify->prop_state != svn_wc_notify_state_changed) action = 'U';
        if (action) {
            struct incoming_change *row = &APR_ARRAY_PUSH(b->changes, struct incoming_change);
            row->path = apr_pstrdup(b->pool, path);
            row->action = action;
        }
    }
}

static void print_update_receipt(svn_revnum_t revision,
                                 apr_array_header_t *conflicts,
                                 apr_array_header_t *changes)
{
    int i;
    printf("{\"schema\":\"" FILEES_SVN_SCHEMA "\",\"ok\":true,\"revision\":");
    if (SVN_IS_VALID_REVNUM(revision)) printf("%ld", (long)revision);
    else printf("null");
    printf(",\"conflicts\":[");
    for (i = 0; i < conflicts->nelts; ++i) {
        if (i) putchar(',');
        filees_json_string(APR_ARRAY_IDX(conflicts, i, const char *));
    }
    printf("],\"changes\":[");
    for (i = 0; i < changes->nelts; ++i) {
        const struct incoming_change *row = &APR_ARRAY_IDX(changes, i, struct incoming_change);
        if (i) putchar(',');
        printf("{\"path\":"); filees_json_string(row->path);
        printf(",\"action\":\"%c\"}", row->action);
    }
    puts("]}");
}

/* filees_ra_checkout creates a working copy.
 *
 * The marker guard the other working-copy verbs stand on cannot apply: there is
 * no working copy yet, and .filees is written afterwards. What is checked
 * instead is that the destination is an absolute path with no symlink in its
 * parent chain, and that it is not already a working copy - a checkout over an
 * existing one is a different operation with a different failure mode, and the
 * caller decides between them rather than discovering which it got.
 *
 * force allows unversioned obstructions, which is how an existing folder is
 * adopted on first import. Subversion still refuses versioned ones. */
svn_error_t *filees_ra_checkout(const char *url_arg, const char *wc_arg,
                                svn_revnum_t revision, svn_boolean_t force,
                                apr_pool_t *pool)
{
    const char *url, *wc;
    svn_client_ctx_t *ctx;
    svn_opt_revision_t peg, rev;
    struct notify_baton notify;
    svn_revnum_t result = SVN_INVALID_REVNUM;
    apr_finfo_t info;

    SVN_ERR(filees_ra_target(&url, url_arg, pool));
    if (!wc_arg || !*wc_arg) return filees_refuse("--wc must be an absolute path");
    wc = svn_dirent_internal_style(wc_arg, pool);
    if (!svn_dirent_is_absolute(wc)) return filees_refuse("--wc must be an absolute path");
    if (apr_stat(&info, svn_dirent_join(wc, ".svn", pool), APR_FINFO_TYPE, pool) == APR_SUCCESS)
        return filees_refuse("destination is already a working copy");
    if (apr_stat(&info, wc, APR_FINFO_TYPE, pool) == APR_SUCCESS)
        SVN_ERR(filees_plain_node(wc, APR_DIR, FALSE, pool));

    peg.kind = svn_opt_revision_unspecified;
    if (SVN_IS_VALID_REVNUM(revision)) {
        rev.kind = svn_opt_revision_number;
        rev.value.number = revision;
    } else {
        rev.kind = svn_opt_revision_head;
    }

    SVN_ERR(filees_ra_ctx(&ctx, pool));
    notify.wc = wc;
    notify.conflicts = apr_array_make(pool, 4, sizeof(const char *));
    notify.changes = apr_array_make(pool, 16, sizeof(struct incoming_change));
    notify.pool = pool;
    ctx->notify_func2 = collect_notify;
    ctx->notify_baton2 = &notify;

    SVN_ERR(svn_client_checkout3(&result, url, wc, &peg, &rev, svn_depth_infinity,
                                 TRUE /* ignore_externals */, force, ctx, pool));
    print_update_receipt(result, notify.conflicts, notify.changes);
    return SVN_NO_ERROR;
}

/* filees_ra_update brings a working copy forward.
 *
 * Depth is svn_depth_unknown for a whole-tree update, which means "respect what
 * each directory already records" - not "infinity". Passing infinity would
 * quietly deepen a sparse checkout, turning an update into a download nobody
 * asked for. --depth empty targets named paths without doing that either, and
 * depth is never sticky here: this verb reports history, it does not redefine
 * what the working copy is. */
svn_error_t *filees_ra_update(const char *wc_arg, svn_boolean_t live,
                              const char **rels, int n, svn_depth_t depth,
                              svn_revnum_t revision, apr_pool_t *pool)
{
    const char *wc;
    svn_client_ctx_t *ctx;
    svn_opt_revision_t rev;
    apr_array_header_t *paths, *result_revs;
    struct notify_baton notify;
    svn_revnum_t result = SVN_INVALID_REVNUM;
    int i;

    SVN_ERR(filees_require_wc(&wc, &ctx, wc_arg, live, pool));
    SVN_ERR(filees_ra_ctx_auth(ctx, pool));

    if (n == 0) {
        paths = apr_array_make(pool, 1, sizeof(const char *));
        APR_ARRAY_PUSH(paths, const char *) = wc;
    } else {
        SVN_ERR(filees_abs_paths(&paths, wc, rels, n, pool));
    }

    if (SVN_IS_VALID_REVNUM(revision)) {
        rev.kind = svn_opt_revision_number;
        rev.value.number = revision;
    } else {
        rev.kind = svn_opt_revision_head;
    }

    notify.wc = wc;
    notify.conflicts = apr_array_make(pool, 4, sizeof(const char *));
    notify.changes = apr_array_make(pool, 16, sizeof(struct incoming_change));
    notify.pool = pool;
    ctx->notify_func2 = collect_notify;
    ctx->notify_baton2 = &notify;

    SVN_ERR(svn_client_update4(&result_revs, paths, &rev, depth,
                               FALSE /* depth_is_sticky */,
                               TRUE /* ignore_externals */,
                               FALSE /* allow_unver_obstructions */,
                               TRUE /* adds_as_modification */,
                               FALSE /* make_parents */,
                               ctx, pool));
    for (i = 0; result_revs && i < result_revs->nelts; ++i) {
        svn_revnum_t one = APR_ARRAY_IDX(result_revs, i, svn_revnum_t);
        if (SVN_IS_VALID_REVNUM(one) && (!SVN_IS_VALID_REVNUM(result) || one > result))
            result = one;
    }
    print_update_receipt(result, notify.conflicts, notify.changes);
    return SVN_NO_ERROR;
}

struct repair_observation { const char *path; svn_client_status_t *status; apr_pool_t *pool; };
static svn_error_t *repair_status(void *baton, const char *path,
                                   const svn_client_status_t *status, apr_pool_t *pool)
{
    struct repair_observation *b = baton;
    (void)path; (void)pool;
    if (!strcmp(status->local_abspath, b->path)) b->status = svn_client_status_dup(status, b->pool);
    return SVN_NO_ERROR;
}
static svn_error_t *repair_observe(svn_client_status_t **status, const char *path,
                                   svn_client_ctx_t *ctx, apr_pool_t *pool)
{
    struct repair_observation b = {path, NULL, pool};
    svn_opt_revision_t working;
    working.kind = svn_opt_revision_working;
    SVN_ERR(svn_client_status6(NULL, ctx, path, &working, svn_depth_empty,
                               TRUE, FALSE, TRUE, TRUE, TRUE, FALSE,
                               NULL, repair_status, &b, pool));
    if (!b.status) return filees_refuse("recovery requires an exact WC observation");
    *status = b.status;
    return SVN_NO_ERROR;
}
struct repair_conflicts { apr_hash_t *paths; svn_revnum_t revision; };
static svn_error_t *repair_no_props(void *baton, const char *path,
                                    apr_hash_t *props, apr_array_header_t *inherited,
                                    apr_pool_t *pool)
{
    (void)baton; (void)path; (void)inherited; (void)pool;
    if (apr_hash_count(props)) return filees_refuse("recovery refuses committed properties");
    return SVN_NO_ERROR;
}
static svn_error_t *repair_keep_text(svn_wc_conflict_result_t **result,
                                     const svn_wc_conflict_description2_t *d,
                                     void *baton, apr_pool_t *result_pool,
                                     apr_pool_t *scratch_pool)
{
    struct repair_conflicts *b = baton;
    (void)scratch_pool;
    if (d->kind != svn_wc_conflict_kind_text || d->node_kind != svn_node_file ||
        !apr_hash_get(b->paths, d->local_abspath, APR_HASH_KEY_STRING) ||
        !d->src_right_version || d->src_right_version->peg_rev != b->revision)
        return filees_refuse("recovery refuses an unexpected conflict");
    *result = svn_wc_create_conflict_result(svn_wc_conflict_choose_mine_full, NULL, result_pool);
    return SVN_NO_ERROR;
}

/* A deliberately narrow receipt repair, NOT a general update/resolve mode.
 * Only plain, nonempty, no-property additions are admitted, and only if the
 * exact revision/UUID proves their creation without copy history. Other
 * structural/translation cases retain HOLD instead of guessing. The conflict
 * choice applies during update, not by resolving pre-existing user conflicts.
 * Replay skips targets already at or beyond the receipt, never downgrades. */
svn_error_t *filees_recover_commit(const char *wc_arg, svn_boolean_t live,
                                   const char *url_arg, const char *marker,
                                   svn_revnum_t revision, const char **rels,
                                   int n, apr_pool_t *pool)
{
    const char *wc, *url, *root_url;
    svn_client_ctx_t *ctx;
    svn_client_status_t *root;
    apr_array_header_t *paths, *targets, *ranges, *props, *selected, *revisions;
    apr_hash_t *checksums;
    svn_opt_revision_range_t range;
    svn_opt_revision_t rev;
    struct log_baton log;
    struct log_entry *entry;
    struct repair_conflicts conflicts;
    int i, j, matches = 0;
    if (revision < 1 || !marker || !*marker || n < 1) return filees_refuse("invalid recovery identity");
    SVN_ERR(filees_require_wc(&wc, &ctx, wc_arg, live, pool));
    SVN_ERR(filees_ra_ctx_auth(ctx, pool));
    SVN_ERR(filees_ra_target(&url, url_arg, pool));
    SVN_ERR(filees_abs_paths(&paths, wc, rels, n, pool));
    SVN_ERR(repair_observe(&root, wc, ctx, pool));
    if (!root->repos_root_url || !root->repos_relpath || root->switched || root->conflicted)
        return filees_refuse("recovery WC identity unavailable");
    root_url = svn_path_url_add_component2(root->repos_root_url, root->repos_relpath, pool);
    if (strcmp(url, root_url)) return filees_refuse("recovery URL differs from WC identity");
    rev.kind = svn_opt_revision_number; rev.value.number = revision;
    range.start = range.end = rev;
    targets = apr_array_make(pool, 1, sizeof(const char *));
    APR_ARRAY_PUSH(targets, const char *) = url;
    ranges = apr_array_make(pool, 1, sizeof(svn_opt_revision_range_t *));
    APR_ARRAY_PUSH(ranges, svn_opt_revision_range_t *) = &range;
    props = apr_array_make(pool, 1, sizeof(const char *));
    APR_ARRAY_PUSH(props, const char *) = "filees:commit-id";
    log.pool = pool; log.entries = apr_array_make(pool, 1, sizeof(struct log_entry));
    SVN_ERR(svn_client_log5(targets, &rev, ranges, 1, TRUE, TRUE, FALSE,
                            props, collect_log, &log, ctx, pool));
    if (log.entries->nelts != 1) return filees_refuse("recovery revision unavailable");
    entry = &APR_ARRAY_IDX(log.entries, 0, struct log_entry);
    for (j = 0; j < entry->extra->nelts; ++j) {
        struct log_revprop *p = &APR_ARRAY_IDX(entry->extra, j, struct log_revprop);
        if (!strcmp(p->name, "filees:commit-id") && p->value && !strcmp(p->value, marker)) ++matches;
    }
    if (entry->revision != revision || matches != 1) return filees_refuse("recovery receipt mismatch");
    selected = apr_array_make(pool, n, sizeof(const char *));
    checksums = apr_hash_make(pool);
    conflicts.paths = apr_hash_make(pool); conflicts.revision = revision;
    /* Complete read-only admission before the first update. */
    for (i = 0; i < paths->nelts; ++i) {
        const char *path = APR_ARRAY_IDX(paths, i, const char *);
        const char *key = apr_pstrcat(pool, "/", root->repos_relpath,
                                      *root->repos_relpath ? "/" : "", rels[i], NULL);
        svn_client_status_t *s;
        struct log_path *change = NULL;
        apr_hash_t *local_props;
        svn_checksum_t *checksum;
        apr_finfo_t info;
        SVN_ERR(repair_observe(&s, path, ctx, pool));
        if (!s->versioned || s->conflicted || s->switched || s->file_external || s->wc_is_locked || s->node_status == svn_wc_status_missing)
            return filees_refuse("recovery target conflicted, switched or busy");
        if (SVN_IS_VALID_REVNUM(s->revision) && s->revision >= revision) continue;
        for (j = 0; j < entry->paths->nelts; ++j) {
            struct log_path *p = &APR_ARRAY_IDX(entry->paths, j, struct log_path);
            if (!strcmp(p->path, key)) change = p;
        }
        if (!change) continue; /* Selected by original commit, but unchanged. */
        if (change->action != 'A' || change->copyfrom_path || strcmp(change->kind, "file") ||
            !s->versioned || s->kind != svn_node_file || s->node_status != svn_wc_status_added || s->copied)
            return filees_refuse("recovery currently requires a plain committed addition");
        SVN_ERR(filees_plain_node(path, APR_REG, FALSE, pool));
        if (apr_stat(&info, path, APR_FINFO_SIZE, pool) != APR_SUCCESS || info.size <= 0)
            return filees_refuse("recovery refuses missing or empty working text");
        SVN_ERR(svn_wc_prop_list2(&local_props, ctx->wc_ctx, path, pool, pool));
        if (apr_hash_count(local_props)) return filees_refuse("recovery refuses property-bearing additions");
        SVN_ERR(svn_client_proplist4(svn_path_url_add_component2(url, rels[i], pool),
                                     &rev, &rev, svn_depth_empty, NULL, FALSE,
                                     repair_no_props, NULL, ctx, pool));
        SVN_ERR(svn_io_file_checksum2(&checksum, path, svn_checksum_sha1, pool));
        apr_hash_set(checksums, path, APR_HASH_KEY_STRING, checksum);
        APR_ARRAY_PUSH(selected, const char *) = path;
        apr_hash_set(conflicts.paths, path, APR_HASH_KEY_STRING, path);
    }
    ctx->conflict_func2 = repair_keep_text; ctx->conflict_baton2 = &conflicts;
    if (selected->nelts)
        SVN_ERR(svn_client_update4(&revisions, selected, &rev, svn_depth_empty,
                                   FALSE, TRUE, FALSE, TRUE, FALSE, ctx, pool));
    /* Exit zero is not proof of reconciliation: postponed conflicts must not
     * become a daemon ACK. A concurrent writer also retains the intent. */
    for (i = 0; i < selected->nelts; ++i) {
        const char *path = APR_ARRAY_IDX(selected, i, const char *);
        svn_client_status_t *s;
        svn_checksum_t *after;
        SVN_ERR(repair_observe(&s, path, ctx, pool));
        if (s->conflicted || s->revision != revision ||
            (s->node_status != svn_wc_status_normal && s->node_status != svn_wc_status_modified))
            return filees_refuse("recovery metadata remains unresolved");
        SVN_ERR(svn_io_file_checksum2(&after, path, svn_checksum_sha1, pool));
        if (!svn_checksum_match(after, apr_hash_get(checksums, path, APR_HASH_KEY_STRING)))
            return filees_refuse("working text changed during recovery; intent must be retained");
    }
    printf("{\"schema\":\"" FILEES_SVN_SCHEMA "\",\"ok\":true,\"revision\":%ld,\"reconciled\":%d}\n",
           (long)revision, selected->nelts);
    return SVN_NO_ERROR;
}

struct lock_result {
    const char *path;
    const char *failure; /* NULL when it worked */
};

struct lock_baton {
    const char *wc;
    apr_array_header_t *results; /* struct lock_result */
    apr_pool_t *pool;
};

/* Lock and unlock do not fail as a whole when one path is refused: Subversion
 * reports the refusal through a notification and carries on. Reporting only the
 * overall exit status would therefore turn "somebody else holds this file" into
 * silence, which is the one thing a reservation must never be. */
static void collect_lock(void *baton, const svn_wc_notify_t *notify,
                         apr_pool_t *pool)
{
    struct lock_baton *b = baton;
    struct lock_result *row;
    const char *path;
    svn_boolean_t failed;
    (void)pool;

    switch (notify->action) {
    case svn_wc_notify_locked:
    case svn_wc_notify_unlocked:
        failed = FALSE;
        break;
    case svn_wc_notify_failed_lock:
    case svn_wc_notify_failed_unlock:
        failed = TRUE;
        break;
    default:
        return;
    }
    if (!notify->path) return;
    path = notify->path;
    if (b->wc && svn_dirent_is_absolute(path)) {
        const char *rel = svn_dirent_skip_ancestor(b->wc, path);
        if (rel && *rel) path = rel;
    }
    row = apr_array_push(b->results);
    row->path = apr_pstrdup(b->pool, path);
    row->failure = NULL;
    if (failed) {
        char buf[512];
        row->failure = notify->err
                           ? apr_pstrdup(b->pool, svn_err_best_message(notify->err, buf, sizeof(buf)))
                           : apr_pstrdup(b->pool, "refused");
    }
}

static void print_lock_receipt(const char *field, apr_array_header_t *results)
{
    int i;
    printf("{\"schema\":\"" FILEES_SVN_SCHEMA "\",\"ok\":true,\"%s\":[", field);
    for (i = 0; i < results->nelts; ++i) {
        struct lock_result *row = &APR_ARRAY_IDX(results, i, struct lock_result);
        if (i) putchar(',');
        printf("{\"path\":");
        filees_json_string(row->path);
        printf(",\"ok\":%s,\"error\":", row->failure ? "false" : "true");
        if (row->failure) filees_json_string(row->failure);
        else printf("null");
        putchar('}');
    }
    puts("]}");
}

static svn_error_t *supply_log_message(const char **log_msg, const char **tmp_file,
                                      const apr_array_header_t *commit_items,
                                      void *baton, apr_pool_t *pool)
{
    (void)commit_items;
    (void)pool;
    *log_msg = baton;
    *tmp_file = NULL;
    return SVN_NO_ERROR;
}

struct commit_baton {
    svn_revnum_t revision;
    const char *date;
    const char *author;
    apr_pool_t *pool;
};

static svn_error_t *collect_commit(const svn_commit_info_t *info, void *baton,
                                   apr_pool_t *pool)
{
    struct commit_baton *b = baton;
    (void)pool;
    b->revision = info->revision;
    b->date = info->date ? apr_pstrdup(b->pool, info->date) : NULL;
    b->author = info->author ? apr_pstrdup(b->pool, info->author) : NULL;
    return SVN_NO_ERROR;
}

/* filees_ra_commit publishes a named set of paths.
 *
 * depth is empty and commit_as_operations is TRUE, which together mean "these
 * paths and nothing else". A commit that quietly widened its own scope would
 * publish work the caller never listed - and FileES builds its batches
 * deliberately, filtering ignored files and withheld deletions on the way.
 *
 * The revision comes from the commit callback rather than from a second
 * question to the server. pkg/client currently reads HEAD before and after and
 * matches a filees:commit-id marker to be sure which revision was its own; the
 * marker still works here (--revprop), but the callback answers directly.
 *
 * An empty commit is not an error: Subversion produces no revision and the
 * receipt says null. The caller decides whether that was expected. */
svn_error_t *filees_ra_commit(const char *wc_arg, svn_boolean_t live,
                              const char **rels, int n, const char *message,
                              svn_boolean_t keep_locks, const char **revprops,
                              int nrevprops, apr_pool_t *pool)
{
    const char *wc;
    svn_client_ctx_t *ctx;
    apr_array_header_t *paths;
    apr_hash_t *revprop_table = NULL;
    struct commit_baton commit;
    int i;

    if (n < 1) return filees_refuse("commit requires at least one path");
    if (!message) return filees_refuse("commit requires -m");
    SVN_ERR(filees_require_wc(&wc, &ctx, wc_arg, live, pool));
    SVN_ERR(filees_ra_ctx_auth(ctx, pool));
    SVN_ERR(filees_abs_paths(&paths, wc, rels, n, pool));

    if (nrevprops) {
        revprop_table = apr_hash_make(pool);
        for (i = 0; i < nrevprops; ++i) {
            const char *eq = strchr(revprops[i], '=');
            if (!eq || eq == revprops[i]) return filees_refuse("--revprop expects NAME=VALUE");
            /* One source for the message. Letting --revprop set svn:log too
             * would let it quietly override -m, and the caller would have no
             * way to see which one the repository got. */
            if (!strncmp(revprops[i], SVN_PROP_REVISION_LOG "=", strlen(SVN_PROP_REVISION_LOG) + 1))
                return filees_refuse("the message is -m; --revprop must not set svn:log");
            apr_hash_set(revprop_table,
                         apr_pstrndup(pool, revprops[i], (apr_size_t)(eq - revprops[i])),
                         APR_HASH_KEY_STRING,
                         svn_string_create(eq + 1, pool));
        }
    }

    /* The message travels through log_msg_func3, not the revprop table.
     * svn_client_commit6 has no message parameter, and setting svn:log
     * directly is refused outright (E195011, "Standard properties can't be set
     * explicitly as revision properties"). Measured 2026-09-08 - first without
     * any message at all, when every commit succeeded with an empty svn:log,
     * which would have silently emptied the Shouting Commit lane because
     * announcements ride in exactly that property. */
    ctx->log_msg_func3 = supply_log_message;
    ctx->log_msg_baton3 = (void *)message;

    commit.revision = SVN_INVALID_REVNUM;
    commit.date = NULL;
    commit.author = NULL;
    commit.pool = pool;
    SVN_ERR(svn_client_commit6(paths, svn_depth_empty, keep_locks,
                               FALSE /* keep_changelists */,
                               TRUE /* commit_as_operations */,
                               FALSE /* include_file_externals */,
                               FALSE /* include_dir_externals */,
                               NULL /* changelists */, revprop_table,
                               collect_commit, &commit, ctx, pool));

    printf("{\"schema\":\"" FILEES_SVN_SCHEMA "\",\"ok\":true,\"revision\":");
    if (SVN_IS_VALID_REVNUM(commit.revision)) printf("%ld", (long)commit.revision);
    else printf("null");
    printf(",\"date\":");
    if (commit.date) filees_json_string(commit.date);
    else printf("null");
    printf(",\"author\":");
    if (commit.author) filees_json_string(commit.author);
    else printf("null");
    puts("}");
    return SVN_NO_ERROR;
}

/* filees_ra_lock and filees_ra_unlock reserve and release.
 *
 * Neither steals nor breaks. Subversion offers both, and the inventory in
 * concepts/DESKTOP_SVN_CLIENT_SCOPE.md deliberately does not: taking a
 * reservation away from whoever holds it is not an accepted migration
 * mechanism here, and a capability the product has not accepted has no business
 * existing in the binary that would make it one keystroke away. */
svn_error_t *filees_ra_lock(const char *wc_arg, svn_boolean_t live,
                            const char **rels, int n, const char *comment,
                            apr_pool_t *pool)
{
    const char *wc;
    svn_client_ctx_t *ctx;
    apr_array_header_t *paths;
    struct lock_baton b;

    if (n < 1) return filees_refuse("lock requires at least one path");
    SVN_ERR(filees_require_wc(&wc, &ctx, wc_arg, live, pool));
    SVN_ERR(filees_ra_ctx_auth(ctx, pool));
    SVN_ERR(filees_abs_paths(&paths, wc, rels, n, pool));
    b.wc = wc;
    b.results = apr_array_make(pool, n, sizeof(struct lock_result));
    b.pool = pool;
    ctx->notify_func2 = collect_lock;
    ctx->notify_baton2 = &b;
    SVN_ERR(svn_client_lock(paths, comment, FALSE /* steal_lock */, ctx, pool));
    print_lock_receipt("locked", b.results);
    return SVN_NO_ERROR;
}

svn_error_t *filees_ra_unlock(const char *wc_arg, svn_boolean_t live,
                              const char **rels, int n, apr_pool_t *pool)
{
    const char *wc;
    svn_client_ctx_t *ctx;
    apr_array_header_t *paths;
    struct lock_baton b;

    if (n < 1) return filees_refuse("unlock requires at least one path");
    SVN_ERR(filees_require_wc(&wc, &ctx, wc_arg, live, pool));
    SVN_ERR(filees_ra_ctx_auth(ctx, pool));
    SVN_ERR(filees_abs_paths(&paths, wc, rels, n, pool));
    b.wc = wc;
    b.results = apr_array_make(pool, n, sizeof(struct lock_result));
    b.pool = pool;
    ctx->notify_func2 = collect_lock;
    ctx->notify_baton2 = &b;
    SVN_ERR(svn_client_unlock(paths, FALSE /* break_lock */, ctx, pool));
    print_lock_receipt("unlocked", b.results);
    return SVN_NO_ERROR;
}
