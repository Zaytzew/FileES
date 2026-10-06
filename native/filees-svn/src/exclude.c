/* Internal sparse-WC release primitive. The caller must preserve an exact
 * copy outside WC and serialize its transaction under the WC guard. This
 * does not coordinate external editors, restore CFAPI identity or retire
 * any backup. A failure (even after mutation) must retain that copy. */
#include "filees_svn.h"

#include <stdio.h>
#include <string.h>
#include <apr_strings.h>
#include <svn_dirent_uri.h>
#include <svn_props.h>
#ifdef _WIN32
#include <windows.h>

static apr_status_t close_retained_guard(void *baton)
{
    CloseHandle((HANDLE)baton);
    return APR_SUCCESS;
}

/* Admit ordinary local attributes before opening a data-read guard. Windows
 * does not enforce sharing for an attributes-only handle: GENERIC_READ is
 * needed to deny concurrent writes; the retained copy also denies deletion.
 * The source allows deletion for SVN. This is not a lock on its directory
 * entry: rename/replacement races still belong to the transaction caller. */
static svn_error_t *guard_file(const char *path, svn_boolean_t retained, apr_pool_t *pool)
{
    int count = MultiByteToWideChar(CP_UTF8, MB_ERR_INVALID_CHARS, path, -1, NULL, 0);
    WCHAR *wide;
    HANDLE handle;
    HANDLE read_guard;
    BY_HANDLE_FILE_INFORMATION info, guarded_info;
    DWORD forbidden = FILE_ATTRIBUTE_DIRECTORY | FILE_ATTRIBUTE_REPARSE_POINT |
                      FILE_ATTRIBUTE_OFFLINE | 0x00040000 /* RECALL_ON_OPEN */ |
                      0x00400000 /* RECALL_ON_DATA_ACCESS */;
    if (!count) return filees_refuse("invalid UTF-8 release path");
    wide = apr_palloc(pool, (size_t)count * sizeof(WCHAR));
    if (!MultiByteToWideChar(CP_UTF8, MB_ERR_INVALID_CHARS, path, -1, wide, count))
        return filees_refuse("cannot convert release path");
    handle = CreateFileW(wide, FILE_READ_ATTRIBUTES,
                         FILE_SHARE_READ | (retained ? 0 : FILE_SHARE_DELETE),
                         NULL, OPEN_EXISTING, FILE_FLAG_OPEN_REPARSE_POINT, NULL);
    if (handle == INVALID_HANDLE_VALUE)
        return filees_refuse("release file is busy or unavailable");
    apr_pool_cleanup_register(pool, handle, close_retained_guard, apr_pool_cleanup_null);
    if (!GetFileInformationByHandle(handle, &info) || (info.dwFileAttributes & forbidden))
        return filees_refuse("release requires an ordinary locally materialized file");
    read_guard = CreateFileW(wide, GENERIC_READ,
                            FILE_SHARE_READ | (retained ? 0 : FILE_SHARE_DELETE),
                            NULL, OPEN_EXISTING, FILE_FLAG_OPEN_REPARSE_POINT, NULL);
    if (read_guard == INVALID_HANDLE_VALUE)
        return filees_refuse("release file is open for writing or unavailable");
    apr_pool_cleanup_register(pool, read_guard, close_retained_guard, apr_pool_cleanup_null);
    if (!GetFileInformationByHandle(read_guard, &guarded_info) ||
        (guarded_info.dwFileAttributes & forbidden) ||
        info.dwVolumeSerialNumber != guarded_info.dwVolumeSerialNumber ||
        info.nFileIndexHigh != guarded_info.nFileIndexHigh ||
        info.nFileIndexLow != guarded_info.nFileIndexLow)
        return filees_refuse("release file identity changed during admission");
    return SVN_NO_ERROR;
}
#else
/* FileES writer/WC guards are not a lock against arbitrary external Unix
 * editors. The CFAPI coordinator is Windows-only and not wired here yet. */
static svn_error_t *guard_file(const char *path, svn_boolean_t retained, apr_pool_t *pool)
{
    (void)path; (void)retained; (void)pool;
    return SVN_NO_ERROR;
}
#endif

struct exclude_observation {
    const char *path;
    svn_client_status_t *status;
    apr_pool_t *pool;
};

static svn_boolean_t retained_inside_wc(const char *wc, const char *retained, apr_pool_t *pool)
{
#ifdef _WIN32
    int root_count = MultiByteToWideChar(CP_UTF8, MB_ERR_INVALID_CHARS, wc, -1, NULL, 0);
    int copy_count = MultiByteToWideChar(CP_UTF8, MB_ERR_INVALID_CHARS, retained, -1, NULL, 0);
    WCHAR *root, *copy;
    if (!root_count || !copy_count) return TRUE; /* fail closed */
    if (copy_count < root_count) return FALSE;
    root = apr_palloc(pool, (size_t)root_count * sizeof(WCHAR));
    copy = apr_palloc(pool, (size_t)copy_count * sizeof(WCHAR));
    if (!MultiByteToWideChar(CP_UTF8, MB_ERR_INVALID_CHARS, wc, -1, root, root_count) ||
        !MultiByteToWideChar(CP_UTF8, MB_ERR_INVALID_CHARS, retained, -1, copy, copy_count))
        return TRUE;
    /* Canonical SVN dirents use '/', including on Windows. */
    return CompareStringOrdinal(root, root_count - 1, copy, root_count - 1, TRUE) == CSTR_EQUAL &&
           (copy[root_count - 1] == L'/' || copy[root_count - 1] == L'\0');
#else
    (void)pool;
    return svn_dirent_skip_ancestor(wc, retained) != NULL;
#endif
}

static svn_error_t *exclude_status(void *baton, const char *path,
                                  const svn_client_status_t *status, apr_pool_t *pool)
{
    struct exclude_observation *b = baton;
    (void)path; (void)pool;
    if (!strcmp(b->path, status->local_abspath))
        b->status = svn_client_status_dup(status, b->pool);
    return SVN_NO_ERROR;
}

struct excluded_info {
    const char *path;
    svn_client_info2_t *info;
    apr_pool_t *pool;
};

/* Excluded BASE metadata can precede removal by the durable work queue.
 * SVN retains its write lock when work remains. Observe it through public
 * API; never run cleanup or clear a lock as part of this read-only receipt.
 * This is an idle snapshot, not an interprocess reservation. */
static svn_error_t *require_exclusion_idle(svn_client_ctx_t *ctx,
                                           const char *path, apr_pool_t *pool)
{
    svn_boolean_t locked;
    SVN_ERR(svn_wc_locked2(NULL, &locked, ctx->wc_ctx, path, pool));
    if (locked) return filees_refuse("exclusion inspection refuses an active or unfinished SVN operation");
    return SVN_NO_ERROR;
}

static svn_error_t *collect_excluded_info(void *baton, const char *path,
                                         const svn_client_info2_t *info, apr_pool_t *pool)
{
    struct excluded_info *b = baton;
    (void)pool;
    if (strcmp(path, b->path) || b->info)
        return filees_refuse("exclusion inspection returned another or duplicate node");
    b->info = svn_client_info2_dup(info, b->pool);
    return SVN_NO_ERROR;
}

/* No data handles, status scan, writer record, authentication or RA. The
 * public info API exposes excluded BASE nodes only with fetch_excluded.
 * Both revisions MUST be NULL: even WORKING would select a network branch.
 * This receipt describes WC metadata, not the current filesystem object.
 * A replacement/placeholder at the path is never opened or removed here. */
svn_error_t *filees_inspect_excluded(const char *wc_arg, const char *rel, apr_pool_t *pool)
{
    const char *wc, *path, *root;
    const char *rels[1];
    svn_client_ctx_t *ctx;
    apr_array_header_t *paths;
    struct excluded_info b;
    const svn_client_info2_t *info;
    const svn_wc_info_t *local;
    svn_boolean_t excluded;

    if (!filees_safe_relative(rel)) return filees_refuse("inspection requires one canonical data path");
    SVN_ERR(filees_require_wc(&wc, &ctx, wc_arg, TRUE, pool));
    rels[0] = rel;
    SVN_ERR(filees_abs_paths(&paths, wc, rels, 1, pool));
    path = APR_ARRAY_IDX(paths, 0, const char *);
    SVN_ERR(filees_plain_node(svn_dirent_dirname(path, pool), APR_DIR, FALSE, pool));
    SVN_ERR(svn_client_get_wc_root(&root, path, ctx, pool, pool));
    if (strcmp(root, wc)) return filees_refuse("inspection target belongs to another WC");
    SVN_ERR(require_exclusion_idle(ctx, path, pool));
    b.path = path; b.info = NULL; b.pool = pool;
    SVN_ERR(svn_client_info4(path, NULL, NULL, svn_depth_empty,
                             TRUE, FALSE, FALSE, NULL, collect_excluded_info, &b, ctx, pool));
    info = b.info;
    if (!info || !info->wc_info || info->kind != svn_node_file ||
        !info->URL || !info->repos_root_URL || !info->repos_UUID ||
        !SVN_IS_VALID_REVNUM(info->rev))
        return filees_refuse("inspection requires an exact committed file with known identity and revision");
    local = info->wc_info;
    if (!local->wcroot_abspath || strcmp(local->wcroot_abspath, wc))
        return filees_refuse("inspection WC identity changed");
    excluded = local->depth == svn_depth_exclude && local->schedule == svn_wc_schedule_normal &&
               !local->copyfrom_url && !local->moved_from_abspath && !local->moved_to_abspath &&
               (!local->conflicts || !local->conflicts->nelts);
    SVN_ERR(require_exclusion_idle(ctx, path, pool));
    printf("{\"schema\":\"" FILEES_SVN_SCHEMA "\",\"ok\":true,\"state\":\"%s\",\"path\":",
           excluded ? "excluded" : "not_excluded");
    filees_json_string(rel);
    printf(",\"wc\":"); filees_json_string(wc);
    printf(",\"url\":"); filees_json_string(info->URL);
    printf(",\"repos_root_url\":"); filees_json_string(info->repos_root_URL);
    printf(",\"repos_uuid\":"); filees_json_string(info->repos_UUID);
    printf(",\"revision\":%ld,\"wc_idle\":true}\n", (long)info->rev);
    return SVN_NO_ERROR;
}

svn_error_t *filees_exclude_retained(const char *wc_arg, const char *retained_arg,
                                    const char *rel, apr_pool_t *pool)
{
    const char *wc, *path, *root, *retained;
    const char *rels[1];
    svn_client_ctx_t *ctx;
    svn_client_status_t *s;
    svn_opt_revision_t revision;
    svn_revnum_t against = SVN_INVALID_REVNUM;
    const svn_string_t *property;
    svn_stream_t *pristine, *working;
    struct exclude_observation observed;
    apr_array_header_t *paths, *results;
    svn_node_kind_t kind;
    svn_boolean_t same;
    apr_finfo_t source_info, retained_info;
    apr_status_t info_status;

    if (!filees_safe_relative(rel)) return filees_refuse("exclude requires one canonical data path");
    SVN_ERR(filees_require_wc(&wc, &ctx, wc_arg, TRUE, pool));
    SVN_ERR(filees_writer_guard(wc, pool));
    if (!retained_arg || !svn_dirent_is_absolute(svn_dirent_internal_style(retained_arg, pool)))
        return filees_refuse("exclude requires an absolute retained-copy path");
    SVN_ERR(svn_dirent_get_absolute(&retained, svn_dirent_internal_style(retained_arg, pool), pool));
    if (retained_inside_wc(wc, retained, pool))
        return filees_refuse("retained copy must be outside the WC");
    SVN_ERR(filees_plain_node(retained, APR_REG, FALSE, pool));
    rels[0] = rel;
    SVN_ERR(filees_abs_paths(&paths, wc, rels, 1, pool));
    path = APR_ARRAY_IDX(paths, 0, const char *);
    SVN_ERR(filees_plain_node(path, APR_REG, FALSE, pool));
    SVN_ERR(guard_file(path, FALSE, pool));
    SVN_ERR(guard_file(retained, TRUE, pool));
    info_status = apr_stat(&source_info, path, APR_FINFO_IDENT | APR_FINFO_LINK, pool);
    if (info_status) return svn_error_wrap_apr(info_status, "cannot inspect source identity");
    info_status = apr_stat(&retained_info, retained, APR_FINFO_IDENT | APR_FINFO_LINK, pool);
    if (info_status) return svn_error_wrap_apr(info_status, "cannot inspect retained identity");
    if (source_info.device == retained_info.device && source_info.inode == retained_info.inode)
        return filees_refuse("retained copy must be an independent file");
    SVN_ERR(svn_client_get_wc_root(&root, path, ctx, pool, pool));
    if (strcmp(root, wc)) return filees_refuse("exclude target belongs to another WC");
    SVN_ERR(filees_ra_ctx_auth(ctx, pool));
    revision.kind = svn_opt_revision_working;
    observed.path = path; observed.status = NULL; observed.pool = pool;
    SVN_ERR(svn_client_status6(&against, ctx, path, &revision, svn_depth_empty,
                               TRUE, TRUE, TRUE, TRUE, TRUE, FALSE,
                               NULL, exclude_status, &observed, pool));
    s = observed.status;
    if (!s || !SVN_IS_VALID_REVNUM(against) || !s->versioned ||
        s->kind != svn_node_file || s->node_status != svn_wc_status_normal ||
        !SVN_IS_VALID_REVNUM(s->revision) || s->copied || s->conflicted || s->switched ||
        s->file_external || s->wc_is_locked || s->moved_from_abspath || s->moved_to_abspath ||
        (s->prop_status != svn_wc_status_none && s->prop_status != svn_wc_status_normal))
        return filees_refuse("exclude requires an exact clean committed file without pending changes");
    if (s->lock || s->repos_lock) return filees_refuse("exclude refuses a locally or remotely locked file");
    SVN_ERR(svn_wc_prop_get2(&property, ctx->wc_ctx, path, SVN_PROP_SPECIAL, pool, pool));
    if (property) return filees_refuse("exclude refuses a special file");
    /* First primitive covers untranslated files. Do not approximate SVN's
     * keyword/EOL normalization or trust size/mtime as proof of publication. */
    SVN_ERR(svn_wc_prop_get2(&property, ctx->wc_ctx, path, SVN_PROP_EOL_STYLE, pool, pool));
    if (property) return filees_refuse("exclude does not yet support EOL translation");
    SVN_ERR(svn_wc_prop_get2(&property, ctx->wc_ctx, path, SVN_PROP_KEYWORDS, pool, pool));
    if (property) return filees_refuse("exclude does not yet support keyword translation");
    SVN_ERR(filees_plain_node(path, APR_REG, FALSE, pool));
    SVN_ERR(svn_wc_get_pristine_contents2(&pristine, ctx->wc_ctx, path, pool, pool));
    if (!pristine) return filees_refuse("exclude requires pristine base bytes");
    SVN_ERR(svn_stream_open_readonly(&working, path, pool, pool));
    SVN_ERR(svn_stream_contents_same2(&same, working, pristine, pool));
    if (!same) return filees_refuse("working bytes differ from the committed base");
    SVN_ERR(svn_io_files_contents_same_p(&same, path, retained, pool));
    if (!same) return filees_refuse("retained copy does not match the working file");
    /* Pinned to the WC revision: never fetch an unrelated newer version.
     * Respect the library's normal obstruction/modification handling; no
     * force, adopt, externals or parent expansion. No server write occurs. */
    revision.kind = svn_opt_revision_number;
    revision.value.number = s->revision;
    SVN_ERR(svn_client_update4(&results, paths, &revision, svn_depth_exclude,
                               TRUE, TRUE, FALSE, FALSE, FALSE, ctx, pool));
    SVN_ERR(filees_plain_node(path, APR_REG, TRUE, pool));
    SVN_ERR(svn_wc_read_kind2(&kind, ctx->wc_ctx, path, FALSE, FALSE, pool));
    if (kind != svn_node_none) return filees_refuse("exclude did not remove the visible WC node");
    printf("{\"schema\":\"" FILEES_SVN_SCHEMA "\",\"ok\":true,\"state\":\"excluded\",\"revision\":%ld,\"path\":",
           s->revision);
    filees_json_string(rel);
    puts("}");
    return SVN_NO_ERROR;
}
