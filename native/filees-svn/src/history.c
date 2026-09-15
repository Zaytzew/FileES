/* History reads for Wehikuł czasu (concepts/REPOSITORY_HISTORY_CONCEPT.md).
 *
 * Both verbs take an explicit revision and never default to HEAD: a history
 * read that silently answers for "now" answers the wrong question. The
 * revision is also the peg, so a path means the object that lived at that
 * name in that revision - including paths deleted or moved away since.
 *
 * fetch-file exists because cat cannot meet the raw-bytes contract. With
 * keyword expansion off, svn_client_cat3 still runs a translating stream for
 * any file carrying svn:eol-style (libsvn_client/cat.c, 1.14.5, lines
 * 264-307), so a native file arrives with CRLF on Windows. svn_client_export5
 * translates the same way. svn_ra_get_file hands back the bytes exactly as
 * the repository stores them. */
#include "filees_svn.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include <apr_file_io.h>
#include <apr_strings.h>
#include <svn_dirent_uri.h>
#include <svn_hash.h>
#include <svn_io.h>
#include <svn_props.h>
#include <svn_ra.h>
#include <svn_time.h>

struct list_row {
    const char *name;
    svn_node_kind_t kind;
    svn_filesize_t size;
    svn_revnum_t created_rev;
    apr_time_t time;
    const char *author;
};

struct list_baton {
    apr_array_header_t *rows;  /* struct list_row */
    apr_pool_t *pool;
    svn_boolean_t saw_target;
    svn_node_kind_t target_kind;
};

/* The dirent and its strings live in a scratch pool that is cleared between
 * callbacks, so everything kept is copied into the result pool. */
static svn_error_t *collect_entry(void *baton, const char *path,
                                  const svn_dirent_t *dirent,
                                  const svn_lock_t *lock, const char *abs_path,
                                  const char *external_parent_url,
                                  const char *external_target,
                                  apr_pool_t *scratch_pool)
{
    struct list_baton *b = baton;
    struct list_row *row;
    (void)lock; (void)abs_path; (void)external_parent_url;
    (void)external_target; (void)scratch_pool;

    if (!*path) {
        b->saw_target = TRUE;
        b->target_kind = dirent->kind;
        return SVN_NO_ERROR;
    }
    row = apr_array_push(b->rows);
    row->name = apr_pstrdup(b->pool, path);
    row->kind = dirent->kind;
    row->size = dirent->size;
    row->created_rev = dirent->created_rev;
    row->time = dirent->time;
    row->author = dirent->last_author ? apr_pstrdup(b->pool, dirent->last_author) : NULL;
    return SVN_NO_ERROR;
}

static int compare_rows(const void *left, const void *right)
{
    return strcmp(((const struct list_row *)left)->name,
                  ((const struct list_row *)right)->name);
}

static const char *history_kind(svn_node_kind_t kind)
{
    switch (kind) {
    case svn_node_file: return "file";
    case svn_node_dir: return "dir";
    default: return "unknown";
    }
}

svn_error_t *filees_history_list(const char *url_arg, svn_revnum_t revision,
                                 apr_pool_t *pool)
{
    const char *url;
    svn_client_ctx_t *ctx;
    svn_opt_revision_t peg;
    struct list_baton b;
    int i;

    if (!SVN_IS_VALID_REVNUM(revision))
        return filees_refuse("list requires --revision; history reads never default to HEAD");
    SVN_ERR(filees_ra_target(&url, url_arg, pool));
    SVN_ERR(filees_ra_ctx(&ctx, pool));

    peg.kind = svn_opt_revision_number;
    peg.value.number = revision;
    b.rows = apr_array_make(pool, 32, sizeof(struct list_row));
    b.pool = pool;
    b.saw_target = FALSE;
    b.target_kind = svn_node_unknown;

    SVN_ERR(svn_client_list4(url, &peg, &peg, NULL, svn_depth_immediates,
                             SVN_DIRENT_KIND | SVN_DIRENT_SIZE | SVN_DIRENT_CREATED_REV
                                 | SVN_DIRENT_TIME | SVN_DIRENT_LAST_AUTHOR,
                             FALSE /* fetch_locks */, FALSE /* include_externals */,
                             collect_entry, &b, ctx, pool));
    if (!b.saw_target || b.target_kind != svn_node_dir)
        return filees_refuse("list takes a directory; use fetch-file for a file");

    qsort(b.rows->elts, (size_t)b.rows->nelts, sizeof(struct list_row), compare_rows);

    printf("{\"schema\":\"" FILEES_SVN_SCHEMA "\",\"ok\":true,\"revision\":%ld,\"entries\":[",
           (long)revision);
    for (i = 0; i < b.rows->nelts; ++i) {
        const struct list_row *row = &APR_ARRAY_IDX(b.rows, i, struct list_row);
        if (i) putchar(',');
        printf("{\"name\":");
        filees_json_string(row->name);
        printf(",\"kind\":\"%s\",\"size\":", history_kind(row->kind));
        if (row->kind == svn_node_file && row->size != SVN_INVALID_FILESIZE)
            printf("%" SVN_FILESIZE_T_FMT, row->size);
        else
            printf("null");
        printf(",\"last_changed_revision\":%ld,\"last_changed_date\":", (long)row->created_rev);
        if (row->time) filees_json_string(svn_time_to_cstring(row->time, pool));
        else printf("null");
        printf(",\"last_author\":");
        if (row->author) filees_json_string(row->author);
        else printf("null");
        putchar('}');
    }
    puts("]}");
    return SVN_NO_ERROR;
}

svn_error_t *filees_history_fetch_file(const char *url_arg, const char *out_path,
                                       svn_revnum_t revision, apr_pool_t *pool)
{
    const char *url, *partial;
    svn_client_ctx_t *ctx;
    svn_ra_session_t *session;
    svn_node_kind_t kind;
    apr_hash_t *props;
    apr_file_t *file;
    svn_stream_t *stream;
    apr_finfo_t finfo;
    apr_status_t status;

    if (!SVN_IS_VALID_REVNUM(revision))
        return filees_refuse("fetch-file requires --revision; history reads never default to HEAD");
    SVN_ERR(filees_ra_target(&url, url_arg, pool));
    if (!out_path || !*out_path) return filees_refuse("--out must be an absolute path");
    out_path = svn_dirent_internal_style(out_path, pool);
    if (!svn_dirent_is_absolute(out_path))
        return filees_refuse("--out must be an absolute path");
    {
        apr_finfo_t existing;
        if (apr_stat(&existing, out_path, APR_FINFO_TYPE, pool) == APR_SUCCESS)
            return filees_refuse("--out already exists; this verb never overwrites");
    }
    SVN_ERR(filees_plain_node(out_path, APR_REG, TRUE, pool));

    SVN_ERR(filees_ra_ctx(&ctx, pool));
    SVN_ERR(svn_client_open_ra_session2(&session, url, NULL, ctx, pool, pool));

    /* The session is anchored at the file itself, so "" names it. */
    SVN_ERR(svn_ra_check_path(session, "", revision, &kind, pool));
    if (kind == svn_node_none)
        return filees_refuse("path did not exist in that revision");
    if (kind != svn_node_file)
        return filees_refuse("fetch-file takes a file; use list for a directory");

    /* Properties first, without content: a symbolic link is stored as a
     * "link TARGET" text, and writing that out as an ordinary file would
     * silently turn a link into data. The export policy for special nodes is
     * still open in the concept, so refuse rather than guess. */
    SVN_ERR(svn_ra_get_file(session, "", revision, NULL, NULL, &props, pool));
    if (svn_hash_gets(props, SVN_PROP_SPECIAL))
        return filees_refuse("special node (symbolic link) is not exported");

    partial = apr_pstrcat(pool, out_path, ".part", NULL);
    status = apr_file_open(&file, partial,
                           APR_FOPEN_CREATE | APR_FOPEN_WRITE | APR_FOPEN_EXCL | APR_FOPEN_BINARY,
                           APR_FPROT_UREAD | APR_FPROT_UWRITE, pool);
    if (status != APR_SUCCESS)
        return svn_error_wrap_apr(status, "cannot create %s", partial);
    stream = svn_stream_from_aprfile2(file, FALSE, pool);
    {
        /* A failed fetch leaves nothing behind, as with cat: a surviving
         * .part would make the retry fail on the exclusive open instead of
         * on the real reason. */
        svn_error_t *err = svn_ra_get_file(session, "", revision, stream, NULL, NULL, pool);
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

    printf("{\"schema\":\"" FILEES_SVN_SCHEMA "\",\"ok\":true,\"bytes\":%" APR_OFF_T_FMT
           ",\"revision\":%ld}\n", finfo.size, (long)revision);
    return SVN_NO_ERROR;
}
