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
