/* Offline acknowledgement of ONE independently receipted remote file deletion.
 * Never fetch, open, unlink or ACK another file. The coordinator owns receipt,
 * CFAPI FileIDs/completion, durable fence and scanner/manifest compare-and-swap.
 * A path recreated after completion remains untouched (keep_local=TRUE).
 */
#include "filees_svn.h"
#include <stdio.h>
#include <string.h>
#include <apr_hash.h>
#include <svn_dirent_uri.h>
#include <svn_path.h>
#include <svn_string.h>

#if defined(__GNUC__)
#pragma GCC diagnostic ignored "-Wdeprecated-declarations"
#endif

struct delete_binding {
    const char *wc, *root, *uuid, *selected;
    const svn_client_info2_t *info;
    apr_pool_t *pool;
    int count;
};

static svn_error_t *delete_info(void *baton, const char *path,
    const svn_client_info2_t *info, apr_pool_t *pool)
{
    struct delete_binding *b = baton;
    const char *rel = svn_dirent_skip_ancestor(b->wc, path);
    const svn_wc_info_t *w = info->wc_info;
    if (++b->count != 1 || strcmp(path, b->selected) || !rel || !w ||
        !w->wcroot_abspath || strcmp(w->wcroot_abspath, b->wc) ||
        !info->URL || !info->repos_root_URL || !info->repos_UUID ||
        strcmp(info->repos_root_URL, b->root) || strcmp(info->repos_UUID, b->uuid) ||
        strcmp(info->URL, svn_path_url_add_component2(b->root, rel, pool)) ||
        (w->conflicts && w->conflicts->nelts) || w->copyfrom_url ||
        w->moved_from_abspath || w->moved_to_abspath || info->lock)
        return filees_refuse("file delete metadata: foreign binding, conflict, move, copy or lock");
    b->info = svn_client_info2_dup(info, b->pool);
    return SVN_NO_ERROR;
}

/* NULL revisions keep this offline, including a missing file. */
static svn_error_t *delete_read(const svn_client_info2_t **out,
    struct delete_binding *b, const char *path, svn_client_ctx_t *ctx, apr_pool_t *pool)
{
    svn_error_t *err;
    b->selected = path; b->info = NULL; b->count = 0;
    err = svn_client_info4(path, NULL, NULL, svn_depth_empty, TRUE, TRUE, FALSE,
        NULL, delete_info, b, ctx, pool);
    if (err && (err->apr_err == SVN_ERR_WC_PATH_NOT_FOUND ||
                err->apr_err == SVN_ERR_UNVERSIONED_RESOURCE)) {
        svn_error_clear(err); *out = NULL; return SVN_NO_ERROR;
    }
    SVN_ERR(err);
    if (!b->info) return filees_refuse("file delete metadata: exact node not described");
    *out = b->info;
    return SVN_NO_ERROR;
}

static svn_error_t *delete_props(const char *path, svn_client_ctx_t *ctx, apr_pool_t *pool)
{
    apr_hash_t *actual, *pristine;
    apr_hash_index_t *hi;
    SVN_ERR(svn_wc_prop_list2(&actual, ctx->wc_ctx, path, pool, pool));
    SVN_ERR(svn_wc_get_pristine_props(&pristine, ctx->wc_ctx, path, pool, pool));
    if ((actual ? apr_hash_count(actual) : 0) != (pristine ? apr_hash_count(pristine) : 0))
        return filees_refuse("file delete metadata: unpublished properties");
    for (hi = actual ? apr_hash_first(pool, actual) : NULL; hi; hi = apr_hash_next(hi)) {
        const void *key; void *value; apr_ssize_t len;
        const svn_string_t *p;
        apr_hash_this(hi, &key, &len, &value);
        p = pristine ? apr_hash_get(pristine, key, len) : NULL;
        if (!p || !svn_string_compare(value, p))
            return filees_refuse("file delete metadata: unpublished properties");
    }
    return SVN_NO_ERROR;
}

/* Virtual-only files have no WC metadata. Do not materialize them just to
 * delete them. Validate every existing parent, including the exact WC root.
 * A foreign or scheduled parent must never masquerade as an absent file. */
static svn_error_t *delete_parents(struct delete_binding *b, const char *src,
    svn_client_ctx_t *ctx, apr_pool_t *pool)
{
    const char *path = svn_dirent_dirname(src, pool);
    for (;;) {
        const svn_client_info2_t *info;
        SVN_ERR(filees_plain_node(path, APR_DIR, FALSE, pool));
        SVN_ERR(delete_read(&info, b, path, ctx, pool));
        if (!info && !strcmp(path, b->wc))
            return filees_refuse("file delete metadata: WC root metadata missing");
        if (info && (info->kind != svn_node_dir ||
                     info->wc_info->schedule != svn_wc_schedule_normal ||
                     info->wc_info->depth == svn_depth_exclude))
            return filees_refuse("file delete metadata: changed or excluded parent");
        if (info) SVN_ERR(delete_props(path, ctx, pool));
        if (!strcmp(path, b->wc)) break;
        path = svn_dirent_dirname(path, pool);
    }
    return SVN_NO_ERROR;
}

static svn_error_t *delete_bound_file(const svn_client_info2_t *info, svn_revnum_t base,
    svn_wc_schedule_t schedule, const char *src, svn_client_ctx_t *ctx, apr_pool_t *pool)
{
    if (info->kind != svn_node_file || info->rev != base ||
        info->wc_info->schedule != schedule || info->wc_info->depth == svn_depth_exclude)
        return filees_refuse("file delete metadata: expected exact file BASE and schedule");
    /* Public WC property API deliberately refuses scheduled-deleted nodes.
     * Validate properties before staging. Deleted nodes cannot accept propset;
     * ACK removes only this already-deleted metadata, not another node's props. */
    return schedule == svn_wc_schedule_normal ? delete_props(src, ctx, pool) : SVN_NO_ERROR;
}

static svn_error_t *delete_locked(const char **state, const char *phase,
    struct delete_binding *b, const char *src, svn_revnum_t base, svn_revnum_t revision,
    svn_client_ctx_t *ctx, svn_wc_adm_access_t *adm, apr_pool_t *pool)
{
    const svn_client_info2_t *info;
    svn_wc_committed_queue_t *queue;
    SVN_ERR(delete_parents(b, src, ctx, pool));
    SVN_ERR(delete_read(&info, b, src, ctx, pool));
    if (!info) {
        *state = !strcmp(phase, "inspect") ? "virtual" : "complete";
        return SVN_NO_ERROR;
    }
    if (!strcmp(phase, "verify"))
        return filees_refuse("file delete metadata: local acknowledgement incomplete");
    if (!strcmp(phase, "inspect")) {
        SVN_ERR(delete_bound_file(info, base, svn_wc_schedule_normal, src, ctx, pool));
        *state = "present";
        return SVN_NO_ERROR;
    }
    if (!strcmp(phase, "stage")) {
        if (info->wc_info->schedule == svn_wc_schedule_normal) {
            SVN_ERR(delete_bound_file(info, base, svn_wc_schedule_normal, src, ctx, pool));
            /* Keep-local is mandatory. No file-content comparison, filesystem
             * deletion or recursive client operation, even for a recreated path. */
            SVN_ERR(svn_wc_delete3(src, adm, NULL, NULL, NULL, NULL, TRUE, pool));
            SVN_ERR(delete_read(&info, b, src, ctx, pool));
            if (!info) return filees_refuse("file delete metadata: staged node disappeared");
        }
        SVN_ERR(delete_bound_file(info, base, svn_wc_schedule_delete, src, ctx, pool));
        *state = "scheduled";
        return SVN_NO_ERROR;
    }
    SVN_ERR(delete_bound_file(info, base, svn_wc_schedule_delete, src, ctx, pool));
    queue = svn_wc_committed_queue_create(pool);
    SVN_ERR(svn_wc_queue_committed2(queue, src, adm, FALSE, NULL, FALSE, FALSE, NULL, pool));
    SVN_ERR(svn_wc_process_committed_queue(queue, adm, revision, NULL, NULL, pool));
    SVN_ERR(delete_read(&info, b, src, ctx, pool));
    if (info) return filees_refuse("file delete metadata: committed node survived acknowledgement");
    *state = "complete";
    return SVN_NO_ERROR;
}

svn_error_t *filees_local_file_delete(const char *phase, const char *wc_arg,
    svn_boolean_t live, const char *root, const char *uuid, svn_revnum_t base,
    svn_revnum_t revision, const char *source, apr_pool_t *pool)
{
    const char *wc, *canonical, *src, *state = NULL;
    struct delete_binding b;
    svn_client_ctx_t *ctx;
    svn_wc_adm_access_t *adm;
    svn_error_t *err;
    const unsigned char *p;
    if (strcmp(phase, "inspect") && strcmp(phase, "stage") &&
        strcmp(phase, "ack") && strcmp(phase, "verify"))
        return filees_refuse("file delete metadata: invalid phase");
    if (base < 1 || (!strcmp(phase, "inspect") ? revision != 0 : revision <= base) ||
        !uuid || !*uuid || !source || strlen(source) > 4096 || !filees_safe_relative(source))
        return filees_refuse("file delete metadata: invalid binding");
    for (p = (const unsigned char *)source; *p; ++p)
        if (*p < 32 || *p == 127) return filees_refuse("file delete metadata: control in selection");
    SVN_ERR(filees_ra_target(&canonical, root, pool));
    if (strcmp(root, canonical)) return filees_refuse("file delete metadata: noncanonical root URL");
    SVN_ERR(filees_require_wc(&wc, &ctx, wc_arg, live, pool));
    SVN_ERR(filees_writer_guard(wc, pool));
    memset(&b, 0, sizeof b);
    b.wc = wc; b.root = root; b.uuid = uuid; b.pool = pool;
    src = svn_dirent_join(wc, source, pool);
    /* Lock and revalidate in every phase. Never release a foreign SVN lock,
     * cleanup the WC, ACK children or change parent revisions. */
    SVN_ERR(svn_wc_adm_open3(&adm, NULL, wc, TRUE, -1, NULL, NULL, pool));
    err = delete_locked(&state, phase, &b, src, base, revision, ctx, adm, pool);
    err = svn_error_compose_create(err, svn_wc_adm_close2(adm, pool));
    SVN_ERR(err);
    printf("{\"schema\":\"" FILEES_SVN_SCHEMA "\",\"ok\":true,\"mutation\":\"delete-file\",\"state\":");
    filees_json_string(state);
    printf(",\"base_revision\":%ld,\"revision\":%ld,\"root_url\":", (long)base, (long)revision);
    filees_json_string(root); printf(",\"uuid\":"); filees_json_string(uuid);
    printf(",\"source\":"); filees_json_string(source); puts("}");
    return SVN_NO_ERROR;
}
