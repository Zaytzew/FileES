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
#include <svn_pools.h>
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

/* ---- Export of a whole tree ------------------------------------------------
 *
 * One process and one RA session per step. Composing list and fetch-file per
 * file would cost an SSH handshake per file, which on a real project is
 * minutes of handshakes before the first byte of data.
 *
 * list-tree writes the plan to a file, one JSON object per line: a full
 * repository listing does not fit the adapter's bounded stdout, and the daemon
 * needs every entry to name, size and check free space before confirmation.
 *
 * fetch-tree takes (repository path, local path) pairs on stdin and writes each
 * file under a destination directory the daemon created and owns. Local names
 * are the daemon's decision (portable names, Windows collisions); this verb
 * only refuses anything that could escape the destination or replace a file. */

static void json_append(svn_stringbuf_t *buf, const char *s)
{
    const unsigned char *p = (const unsigned char *)s;
    svn_stringbuf_appendbyte(buf, '"');
    for (; *p; ++p) {
        if (*p == '"' || *p == '\\') {
            svn_stringbuf_appendbyte(buf, '\\');
            svn_stringbuf_appendbyte(buf, (char)*p);
        } else if (*p < 32) {
            char code[8];
            apr_snprintf(code, sizeof code, "\\u%04x", (unsigned int)*p);
            svn_stringbuf_appendcstr(buf, code);
        } else {
            svn_stringbuf_appendbyte(buf, (char)*p);
        }
    }
    svn_stringbuf_appendbyte(buf, '"');
}

struct tree_baton {
    svn_stream_t *out;
    svn_boolean_t saw_target;
    svn_node_kind_t target_kind;
    apr_int64_t files;
    apr_int64_t dirs;
    apr_int64_t bytes;
};

static svn_error_t *plan_entry(void *baton, const char *path,
                               const svn_dirent_t *dirent,
                               const svn_lock_t *lock, const char *abs_path,
                               const char *external_parent_url,
                               const char *external_target,
                               apr_pool_t *scratch_pool)
{
    struct tree_baton *b = baton;
    svn_stringbuf_t *line;
    apr_size_t len;
    (void)lock; (void)abs_path; (void)external_parent_url; (void)external_target;

    if (!*path) {
        b->saw_target = TRUE;
        b->target_kind = dirent->kind;
        return SVN_NO_ERROR;
    }
    line = svn_stringbuf_create("{\"path\":", scratch_pool);
    json_append(line, path);
    if (dirent->kind == svn_node_dir) {
        svn_stringbuf_appendcstr(line, ",\"kind\":\"dir\"}\n");
        b->dirs++;
    } else if (dirent->kind == svn_node_file && dirent->size != SVN_INVALID_FILESIZE) {
        svn_stringbuf_appendcstr(line, apr_psprintf(scratch_pool, ",\"kind\":\"file\",\"size\":%"
                                                    SVN_FILESIZE_T_FMT "}\n", dirent->size));
        b->files++;
        b->bytes += dirent->size;
    } else {
        return filees_refuse("list-tree met a node of unknown kind or size");
    }
    len = line->len;
    return svn_stream_write(b->out, line->data, &len);
}

static svn_error_t *exclusive_out(apr_file_t **file, const char **partial,
                                  const char *out_path, apr_pool_t *pool)
{
    apr_status_t status;
    apr_finfo_t existing;
    if (apr_stat(&existing, out_path, APR_FINFO_TYPE, pool) == APR_SUCCESS)
        return filees_refuse("--out already exists; this verb never overwrites");
    SVN_ERR(filees_plain_node(out_path, APR_REG, TRUE, pool));
    *partial = apr_pstrcat(pool, out_path, ".part", NULL);
    status = apr_file_open(file, *partial,
                           APR_FOPEN_CREATE | APR_FOPEN_WRITE | APR_FOPEN_EXCL | APR_FOPEN_BINARY,
                           APR_FPROT_UREAD | APR_FPROT_UWRITE, pool);
    if (status != APR_SUCCESS)
        return svn_error_wrap_apr(status, "cannot create %s", *partial);
    return SVN_NO_ERROR;
}

svn_error_t *filees_history_list_tree(const char *url_arg, const char *out_arg,
                                      svn_revnum_t revision, apr_pool_t *pool)
{
    /* partial is always set on exclusive_out's only success path, but that
     * is invisible across the call (GCC on Linux flags it; MSVC did not). */
    const char *url, *out_path, *partial = NULL;
    svn_client_ctx_t *ctx;
    svn_opt_revision_t peg;
    apr_file_t *file;
    struct tree_baton b;
    svn_error_t *err;

    if (!SVN_IS_VALID_REVNUM(revision))
        return filees_refuse("list-tree requires --revision; history reads never default to HEAD");
    SVN_ERR(filees_ra_target(&url, url_arg, pool));
    if (!out_arg || !*out_arg) return filees_refuse("--out must be an absolute path");
    out_path = svn_dirent_internal_style(out_arg, pool);
    if (!svn_dirent_is_absolute(out_path)) return filees_refuse("--out must be an absolute path");
    SVN_ERR(filees_ra_ctx(&ctx, pool));
    SVN_ERR(exclusive_out(&file, &partial, out_path, pool));

    memset(&b, 0, sizeof b);
    b.out = svn_stream_from_aprfile2(file, FALSE, pool);
    b.target_kind = svn_node_unknown;
    peg.kind = svn_opt_revision_number;
    peg.value.number = revision;
    /* Externals are not followed: FileES does not use them, and a history copy
     * must not pull in whatever another URL holds today. */
    err = svn_client_list4(url, &peg, &peg, NULL, svn_depth_infinity,
                           SVN_DIRENT_KIND | SVN_DIRENT_SIZE,
                           FALSE /* fetch_locks */, FALSE /* include_externals */,
                           plan_entry, &b, ctx, pool);
    if (!err && (!b.saw_target || b.target_kind != svn_node_dir))
        err = filees_refuse("list-tree takes a directory; use fetch-file for a file");
    if (!err) err = svn_stream_close(b.out);
    else svn_error_clear(svn_stream_close(b.out));
    if (!err) err = svn_io_file_rename2(partial, out_path, FALSE, pool);
    if (err) {
        svn_error_clear(svn_io_remove_file2(partial, TRUE, pool));
        return err;
    }
    printf("{\"schema\":\"" FILEES_SVN_SCHEMA "\",\"ok\":true,\"revision\":%ld,\"dirs\":%"
           APR_INT64_T_FMT ",\"files\":%" APR_INT64_T_FMT ",\"bytes\":%" APR_INT64_T_FMT "}\n",
           (long)revision, b.dirs, b.files, b.bytes);
    return SVN_NO_ERROR;
}

struct fetched { const char *path; apr_off_t bytes; svn_boolean_t special; };

svn_error_t *filees_history_fetch_tree(const char *url_arg, const char *dest_arg,
                                       svn_revnum_t revision, const char **pairs,
                                       int npairs, apr_pool_t *pool)
{
    const char *url, *dest;
    svn_client_ctx_t *ctx;
    svn_ra_session_t *session;
    apr_array_header_t *done;
    apr_pool_t *iterpool;
    int i, first;

    if (!SVN_IS_VALID_REVNUM(revision))
        return filees_refuse("fetch-tree requires --revision; history reads never default to HEAD");
    SVN_ERR(filees_ra_target(&url, url_arg, pool));
    if (!dest_arg || !*dest_arg) return filees_refuse("--dest must be an absolute directory");
    dest = svn_dirent_internal_style(dest_arg, pool);
    if (!svn_dirent_is_absolute(dest)) return filees_refuse("--dest must be an absolute directory");
    /* The destination and every ancestor must be real directories: a link
     * anywhere on the way could put the copy inside a working copy. */
    SVN_ERR(filees_plain_node(dest, APR_DIR, FALSE, pool));
    if (npairs < 1) return filees_refuse("fetch-tree needs at least one manifest pair");

    SVN_ERR(filees_ra_ctx(&ctx, pool));
    SVN_ERR(svn_client_open_ra_session2(&session, url, NULL, ctx, pool, pool));

    done = apr_array_make(pool, npairs, sizeof(struct fetched));
    iterpool = svn_pool_create(pool);
    for (i = 0; i < npairs; ++i) {
        const char *repo_rel = pairs[2 * i], *local_rel = pairs[2 * i + 1];
        const char *target, *partial;
        apr_file_t *file;
        apr_hash_t *props;
        svn_stream_t *stream;
        apr_finfo_t finfo;
        apr_status_t status;
        svn_error_t *err;
        struct fetched *row;

        finfo.size = 0;
        svn_pool_clear(iterpool);
        target = svn_dirent_join(dest, local_rel, iterpool);
        SVN_ERR(filees_plain_node(target, APR_REG, TRUE, iterpool));
        partial = apr_pstrcat(iterpool, target, ".part", NULL);
        status = apr_file_open(&file, partial,
                               APR_FOPEN_CREATE | APR_FOPEN_WRITE | APR_FOPEN_EXCL | APR_FOPEN_BINARY,
                               APR_FPROT_UREAD | APR_FPROT_UWRITE, iterpool);
        if (status != APR_SUCCESS)
            return svn_error_wrap_apr(status, "cannot create %s", partial);
        stream = svn_stream_from_aprfile2(file, FALSE, iterpool);
        /* Content and properties arrive in one request. A symbolic link is
         * stored as "link TARGET" text; it is removed again and reported, so a
         * link never lands on disk as ordinary data. */
        err = svn_ra_get_file(session, repo_rel, revision, stream, NULL, &props, iterpool);
        if (!err) err = svn_stream_close(stream);
        else svn_error_clear(svn_stream_close(stream));
        if (!err && svn_hash_gets(props, SVN_PROP_SPECIAL)) {
            SVN_ERR(svn_io_remove_file2(partial, FALSE, iterpool));
            row = apr_array_push(done);
            row->path = local_rel;
            row->bytes = 0;
            row->special = TRUE;
            continue;
        }
        if (!err) {
            status = apr_stat(&finfo, partial, APR_FINFO_SIZE, iterpool);
            if (status != APR_SUCCESS) err = svn_error_wrap_apr(status, "cannot measure %s", partial);
        }
        if (!err) err = svn_io_file_rename2(partial, target, FALSE, iterpool);
        if (err) {
            svn_error_clear(svn_io_remove_file2(partial, TRUE, iterpool));
            return err;
        }
        row = apr_array_push(done);
        row->path = local_rel;
        row->bytes = finfo.size;
        row->special = FALSE;
    }
    svn_pool_destroy(iterpool);

    printf("{\"schema\":\"" FILEES_SVN_SCHEMA "\",\"ok\":true,\"revision\":%ld,\"files\":[",
           (long)revision);
    for (i = 0, first = 1; i < done->nelts; ++i) {
        const struct fetched *row = &APR_ARRAY_IDX(done, i, struct fetched);
        if (row->special) continue;
        if (!first) putchar(',');
        first = 0;
        printf("{\"path\":");
        filees_json_string(row->path);
        printf(",\"bytes\":%" APR_OFF_T_FMT "}", row->bytes);
    }
    printf("],\"skipped\":[");
    for (i = 0, first = 1; i < done->nelts; ++i) {
        const struct fetched *row = &APR_ARRAY_IDX(done, i, struct fetched);
        if (!row->special) continue;
        if (!first) putchar(',');
        first = 0;
        printf("{\"path\":");
        filees_json_string(row->path);
        printf(",\"reason\":\"special\"}");
    }
    puts("]}");
    return SVN_NO_ERROR;
}
