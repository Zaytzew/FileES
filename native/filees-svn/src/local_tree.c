/* Reconciliation of an independently RECEIPTED remote tree move. Prepare
 * fetches only missing directory metadata; remaining phases stay offline.
 * No recursive post-commit queue, file reads or data renames here.
 * The coordinator owns physical FileIDs, receipt, scanner and durable phases.
 * Public compatibility APIs supply their own matching WC write lock; never
 * clear a foreign lock or acknowledge descendants' unsent actual changes. */
#include "filees_svn.h"
#include <stdio.h>
#include <string.h>
#include <apr_hash.h>
#include <apr_strings.h>
#include <svn_dirent_uri.h>
#include <svn_path.h>
#include <svn_string.h>

/* Public compatibility WC APIs provide roots-only ACK. Limit suppression to
 * this translation unit, not the rest of the native helper. */
#if defined(__GNUC__)
#pragma GCC diagnostic ignored "-Wdeprecated-declarations"
#endif

struct binding {
    const char *wc, *root, *uuid, *selection;
    svn_revnum_t base;
    apr_pool_t *pool;
    const svn_client_info2_t *exact;
    int count, recursive;
};

static svn_error_t *bound_info(void *baton, const char *path,
                               const svn_client_info2_t *info, apr_pool_t *pool)
{
    struct binding *b = baton;
    const char *rel = svn_dirent_skip_ancestor(b->wc, path);
    const svn_wc_info_t *w = info->wc_info;
    (void)pool;
    if (++b->count > 100000 || !rel || !w || !w->wcroot_abspath ||
        strcmp(w->wcroot_abspath, b->wc) || !info->URL || !info->repos_root_URL ||
        !info->repos_UUID || strcmp(info->repos_root_URL, b->root) ||
        strcmp(info->repos_UUID, b->uuid) ||
        strcmp(info->URL, svn_path_url_add_component2(b->root, rel, pool)) ||
        (w->conflicts && w->conflicts->nelts))
        return filees_refuse("tree metadata: foreign root, URL, UUID or conflicted selection");
    if (!strcmp(path, b->selection))
        b->exact = svn_client_info2_dup(info, b->pool);
    if (b->recursive && (info->rev != b->base || w->schedule != svn_wc_schedule_normal ||
                        w->copyfrom_url || w->moved_from_abspath || w->moved_to_abspath))
        return filees_refuse("tree metadata: selected BASE differs or has a scheduled mutation");
    return SVN_NO_ERROR;
}

/* Unspecified peg/revision is deliberate: WORKING/BASE as explicit revisions
 * can turn client info into a repository request. This must stay offline. */
static svn_error_t *read_bound(const svn_client_info2_t **out, struct binding *b,
                               const char *path, svn_boolean_t recursive,
                               svn_client_ctx_t *ctx, apr_pool_t *pool)
{
    b->selection = path; b->exact = NULL; b->count = 0; b->recursive = recursive;
    SVN_ERR(svn_client_info4(path, NULL, NULL,
        recursive ? svn_depth_infinity : svn_depth_empty, TRUE, TRUE, FALSE,
        NULL, bound_info, b, ctx, pool));
    if (!b->exact) return filees_refuse("tree metadata: exact selected node was not described");
    *out = b->exact;
    return SVN_NO_ERROR;
}

static svn_error_t *plain_dir(const svn_client_info2_t *info)
{
    const svn_wc_info_t *w = info->wc_info;
    if (info->kind != svn_node_dir || w->schedule != svn_wc_schedule_normal ||
        w->copyfrom_url || w->moved_from_abspath || w->moved_to_abspath)
        return filees_refuse("tree metadata: expected a plain committed directory");
    return SVN_NO_ERROR;
}

/* Queueing a root acknowledges its ACTUAL properties, even with recurse=FALSE.
 * Refuse differences under the OWNED SVN write lock, not a preflight-only check.
 * Descendant properties are intentionally NOT queued and remain unsent. */
static svn_error_t *root_props(const char *path, svn_client_ctx_t *ctx, apr_pool_t *pool)
{
    apr_hash_t *actual, *pristine;
    apr_hash_index_t *hi;
    SVN_ERR(svn_wc_prop_list2(&actual, ctx->wc_ctx, path, pool, pool));
    SVN_ERR(svn_wc_get_pristine_props(&pristine, ctx->wc_ctx, path, pool, pool));
    if (!actual || !pristine || apr_hash_count(actual) != apr_hash_count(pristine))
        return filees_refuse("tree metadata: selected root has unpublished properties");
    for (hi = apr_hash_first(pool, actual); hi; hi = apr_hash_next(hi)) {
        const void *key; void *value; apr_ssize_t len;
        const svn_string_t *p;
        apr_hash_this(hi, &key, &len, &value);
        p = apr_hash_get(pristine, key, len);
        if (!p || !svn_string_compare(value, p))
            return filees_refuse("tree metadata: selected root has unpublished properties");
    }
    return SVN_NO_ERROR;
}

static svn_error_t *absent_info(struct binding *b, const char *path,
                               svn_client_ctx_t *ctx, apr_pool_t *pool)
{
    const svn_client_info2_t *info;
    svn_error_t *err = read_bound(&info, b, path, FALSE, ctx, pool);
    if (err && (err->apr_err == SVN_ERR_WC_PATH_NOT_FOUND || err->apr_err == SVN_ERR_UNVERSIONED_RESOURCE)) {
        svn_error_clear(err); return SVN_NO_ERROR;
    }
    if (err) return err;
    return filees_refuse("tree metadata: destination already has SVN metadata");
}

static svn_error_t *scheduled_pair(struct binding *b, const char *src, const char *dst,
                                   svn_client_ctx_t *ctx, apr_pool_t *pool)
{
    const svn_client_info2_t *s, *d;
    const svn_wc_info_t *sw, *dw;
    SVN_ERR(read_bound(&s, b, src, FALSE, ctx, pool));
    SVN_ERR(read_bound(&d, b, dst, FALSE, ctx, pool));
    sw = s->wc_info; dw = d->wc_info;
    if (s->kind != svn_node_dir || d->kind != svn_node_dir || s->rev != b->base ||
        sw->schedule != svn_wc_schedule_delete || dw->schedule != svn_wc_schedule_add ||
        !sw->moved_to_abspath || strcmp(sw->moved_to_abspath, dst) ||
        !dw->moved_from_abspath || strcmp(dw->moved_from_abspath, src) ||
        !dw->copyfrom_url || strcmp(dw->copyfrom_url, svn_path_url_add_component2(b->root,
            svn_dirent_skip_ancestor(b->wc, src), pool)) || dw->copyfrom_rev != b->base)
        return filees_refuse("tree metadata: scheduled copy/delete differs from the receipted move");
    return root_props(dst, ctx, pool);
}

static svn_error_t *ack_locked(struct binding *b, const char *src, const char *dst,
                               svn_revnum_t revision, svn_client_ctx_t *ctx,
                               svn_wc_adm_access_t *adm, apr_pool_t *pool)
{
    svn_wc_committed_queue_t *queue;
    SVN_ERR(scheduled_pair(b, src, dst, ctx, pool));
    queue = svn_wc_committed_queue_create(pool);
    SVN_ERR(svn_wc_queue_committed2(queue, src, adm, FALSE, NULL, FALSE, FALSE, NULL, pool));
    SVN_ERR(svn_wc_queue_committed2(queue, dst, adm, FALSE, NULL, FALSE, FALSE, NULL, pool));
    return svn_wc_process_committed_queue(queue, adm, revision, NULL, NULL, pool);
}

svn_error_t *filees_local_tree(const char *phase, const char *wc_arg, svn_boolean_t live,
    const char *root, const char *uuid, svn_revnum_t base, svn_revnum_t revision,
    const char *source, const char *destination, apr_pool_t *pool)
{
    const char *wc, *src, *dst, *canonical;
    svn_client_ctx_t *ctx;
    const svn_client_info2_t *info;
    struct binding b;
    svn_wc_adm_access_t *adm;
    svn_error_t *err;
    apr_array_header_t *sources;
    int preflight = !strcmp(phase, "inspect") || !strcmp(phase, "prepare");
    const char *state = phase;
    if (strcmp(phase, "prepare") && strcmp(phase, "inspect") && strcmp(phase, "stage") && strcmp(phase, "ack") && strcmp(phase, "verify"))
        return filees_refuse("tree metadata: unknown phase");
    if (base < 1 || (!preflight && revision <= base) || !uuid || !*uuid ||
        !source || !destination || strlen(source) > 4096 || strlen(destination) > 4096 ||
        !filees_safe_relative(source) || !filees_safe_relative(destination) ||
        !strcmp(source, destination) || svn_relpath_skip_ancestor(source, destination) ||
        svn_relpath_skip_ancestor(destination, source))
        return filees_refuse("tree metadata: invalid binding or overlapping selection");
    SVN_ERR(filees_ra_target(&canonical, root, pool));
    if (strcmp(root, canonical)) return filees_refuse("tree metadata: noncanonical root URL");
    SVN_ERR(filees_require_wc(&wc, &ctx, wc_arg, live, pool));
    SVN_ERR(filees_writer_guard(wc, pool));
    memset(&b, 0, sizeof b);
    b.wc = wc; b.root = root; b.uuid = uuid; b.base = base; b.pool = pool;
    src = svn_dirent_join(wc, source, pool); dst = svn_dirent_join(wc, destination, pool);
    SVN_ERR(read_bound(&info, &b, wc, FALSE, ctx, pool)); SVN_ERR(plain_dir(info));
    if (!strcmp(phase, "prepare")) {
        /* Initial virtual directories may not be in the sparse WC metadata.
         * Admit ONLY missing metadata on existing directories. Never deepen
         * an existing directory or update its versioned children for a move. */
        const char *selected[2] = { src, svn_dirent_dirname(dst, pool) };
        int i;
        SVN_ERR(filees_ra_ctx_auth(ctx, pool));
        for (i = 0; i < 2; ++i) {
            const char *ancestor = selected[i];
            apr_array_header_t *paths, *revisions;
            svn_opt_revision_t rev;
            err = read_bound(&info, &b, selected[i], FALSE, ctx, pool);
            if (!err) { SVN_ERR(plain_dir(info)); SVN_ERR(root_props(selected[i],ctx,pool)); continue; }
            if (err->apr_err != SVN_ERR_WC_PATH_NOT_FOUND && err->apr_err != SVN_ERR_UNVERSIONED_RESOURCE) return err;
            svn_error_clear(err);
            SVN_ERR(filees_plain_node(selected[i],APR_DIR,FALSE,pool));
            /* Every already-versioned parent must be bound and clean. */
            while (strcmp(ancestor,wc)) {
                ancestor = svn_dirent_dirname(ancestor,pool);
                err = read_bound(&info,&b,ancestor,FALSE,ctx,pool);
                if (!err) { SVN_ERR(plain_dir(info)); SVN_ERR(root_props(ancestor,ctx,pool)); }
                else if (err->apr_err == SVN_ERR_WC_PATH_NOT_FOUND || err->apr_err == SVN_ERR_UNVERSIONED_RESOURCE) svn_error_clear(err);
                else return err;
            }
            paths=apr_array_make(pool,1,sizeof(const char *)); APR_ARRAY_PUSH(paths,const char *)=selected[i];
            rev.kind=svn_opt_revision_number; rev.value.number=base;
            SVN_ERR(svn_client_update4(&revisions,paths,&rev,svn_depth_empty,TRUE,TRUE,TRUE,TRUE,TRUE,ctx,pool));
            SVN_ERR(read_bound(&info,&b,selected[i],FALSE,ctx,pool)); SVN_ERR(plain_dir(info));
            if (info->rev != base) return filees_refuse("tree metadata: directory preparation has another BASE");
        }
        goto result;
    }
    SVN_ERR(read_bound(&info, &b, svn_dirent_dirname(dst, pool), FALSE, ctx, pool)); SVN_ERR(plain_dir(info));
    /* Resume only exact recognizable states. Receipt and object identity still
     * must be independently checked by the coordinator on every invocation. */
    if (!preflight) {
        err = read_bound(&info, &b, src, FALSE, ctx, pool);
        if (err && (err->apr_err == SVN_ERR_WC_PATH_NOT_FOUND || err->apr_err == SVN_ERR_UNVERSIONED_RESOURCE)) {
            svn_error_clear(err);
            SVN_ERR(read_bound(&info, &b, dst, FALSE, ctx, pool)); SVN_ERR(plain_dir(info));
            if (info->rev != revision) return filees_refuse("tree metadata: completed destination has a foreign revision");
            SVN_ERR(filees_plain_node(src, APR_DIR, TRUE, pool));
            SVN_ERR(filees_plain_node(dst, APR_DIR, FALSE, pool));
            state = "complete";
            goto result;
        }
        SVN_ERR(err);
        if (!strcmp(phase, "stage") && info->wc_info->schedule == svn_wc_schedule_delete) {
            SVN_ERR(scheduled_pair(&b, src, dst, ctx, pool));
            SVN_ERR(filees_plain_node(src, APR_DIR, TRUE, pool));
            SVN_ERR(filees_plain_node(dst, APR_DIR, FALSE, pool));
            state = "scheduled";
            goto result;
        }
        if (!strcmp(phase, "verify")) return filees_refuse("tree metadata: local completion has not been established");
    }
    if (preflight || !strcmp(phase, "stage")) {
        SVN_ERR(read_bound(&info, &b, src, TRUE, ctx, pool)); SVN_ERR(plain_dir(info));
        SVN_ERR(root_props(src, ctx, pool));
        SVN_ERR(absent_info(&b, dst, ctx, pool));
        if (preflight) {
            SVN_ERR(filees_plain_node(src, APR_DIR, FALSE, pool));
            SVN_ERR(filees_plain_node(dst, APR_DIR, TRUE, pool));
        } else {
            SVN_ERR(filees_plain_node(src, APR_DIR, TRUE, pool));
            SVN_ERR(filees_plain_node(dst, APR_DIR, FALSE, pool));
            sources = apr_array_make(pool, 1, sizeof(const char *));
            APR_ARRAY_PUSH(sources, const char *) = src;
            SVN_ERR(svn_client_move7(sources, dst, FALSE, FALSE, TRUE, TRUE, NULL, NULL, NULL, ctx, pool));
            SVN_ERR(scheduled_pair(&b, src, dst, ctx, pool));
        }
    } else {
        SVN_ERR(filees_plain_node(src, APR_DIR, TRUE, pool));
        SVN_ERR(filees_plain_node(dst, APR_DIR, FALSE, pool));
        /* SVN writers cannot change root properties between validation and
         * queue processing. Only this owned lock is released; no cleanup. */
        SVN_ERR(svn_wc_adm_open3(&adm, NULL, wc, TRUE, -1, NULL, NULL, pool));
        err = ack_locked(&b, src, dst, revision, ctx, adm, pool);
        err = svn_error_compose_create(err, svn_wc_adm_close2(adm, pool));
        SVN_ERR(err);
    }
result:
    printf("{\"schema\":\"" FILEES_SVN_SCHEMA "\",\"ok\":true,\"state\":");
    filees_json_string(state);
    printf(",\"base_revision\":%ld,\"revision\":%ld,\"root_url\":", (long)base, (long)revision);
    filees_json_string(root); printf(",\"uuid\":"); filees_json_string(uuid);
    printf(",\"source\":"); filees_json_string(source);
    printf(",\"destination\":"); filees_json_string(destination); puts("}");
    return SVN_NO_ERROR;
}
