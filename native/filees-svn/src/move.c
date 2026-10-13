#include "filees_svn.h"

#include <string.h>

#include <apr_strings.h>
#include <svn_dirent_uri.h>
#include <svn_props.h>

struct observation {
    const char *path;
    apr_pool_t *pool;
    svn_client_status_t *status;
};

static svn_error_t *observe(void *baton, const char *path,
                            const svn_client_status_t *status, apr_pool_t *pool)
{
    struct observation *o = baton;
    (void)path;
    (void)pool;
    if (!strcmp(status->local_abspath, o->path))
        o->status = svn_client_status_dup(status, o->pool);
    return SVN_NO_ERROR;
}

static svn_error_t *read_status(svn_client_status_t **status, const char *path,
                                svn_client_ctx_t *ctx, apr_pool_t *pool)
{
    struct observation o = {path, pool, NULL};
    svn_opt_revision_t rev;
    rev.kind = svn_opt_revision_working;
    SVN_ERR(svn_client_status6(NULL, ctx, path, &rev, svn_depth_empty,
                              TRUE, FALSE, TRUE, TRUE, TRUE, FALSE,
                              NULL, observe, &o, pool));
    if (!o.status) return filees_refuse("SVN did not describe the exact requested path");
    *status = o.status;
    return SVN_NO_ERROR;
}

svn_error_t *filees_unschedule_plain_adds(const char *wc_arg, svn_boolean_t live,
    const char **rels, int n, apr_pool_t *pool)
{
    const char *wc;
    svn_client_ctx_t *ctx;
    apr_array_header_t *targets;
    int i;
    if (n < 1) return filees_refuse("unschedule-add requires selected plain adds");
    SVN_ERR(filees_require_wc(&wc, &ctx, wc_arg, live, pool));
    SVN_ERR(filees_writer_guard(wc, pool));
    SVN_ERR(filees_abs_paths(&targets, wc, rels, n, pool));
    for (i = 0; i < targets->nelts; ++i) {
        const char *p = APR_ARRAY_IDX(targets, i, const char *);
        const char *root;
        svn_client_status_t *s;
        SVN_ERR(svn_client_get_wc_root(&root, p, ctx, pool, pool));
        if (strcmp(root, wc)) return filees_refuse("add belongs to another WC");
        SVN_ERR(read_status(&s, p, ctx, pool));
        if (!s->versioned || s->node_status != svn_wc_status_added ||
            SVN_IS_VALID_REVNUM(s->revision) || s->copied || s->conflicted ||
            s->moved_from_abspath || s->moved_to_abspath ||
            (s->kind != svn_node_file && s->kind != svn_node_dir))
            return filees_refuse("not a plain addition without BASE or ancestry");
        SVN_ERR(filees_plain_node(p, s->kind == svn_node_dir ? APR_DIR : APR_REG, FALSE, pool));
    }
    /* Children must precede selected parents. Nonselected scheduled children
     * make SVN refuse; never recurse or remove their content as a fallback. */
    SVN_ERR(svn_client_revert4(targets, svn_depth_empty, NULL, FALSE,
                              TRUE /* metadata_only */, TRUE /* added_keep_local */, ctx, pool));
    return SVN_NO_ERROR;
}

svn_error_t *filees_record_move(const char *wc_arg, const char *old_rel,
                                const char *new_rel, svn_boolean_t live,
                                const char **state, apr_pool_t *pool)
{
    const char *wc, *root, *src, *dst, *parent;
    svn_client_ctx_t *ctx;
    svn_client_status_t *s, *d, *p;
    const svn_string_t *special;
    apr_array_header_t *sources;
    if (!filees_safe_relative(old_rel) || !filees_safe_relative(new_rel) || !strcmp(old_rel, new_rel))
        return filees_refuse("expected two distinct canonical relative data paths");
    SVN_ERR(filees_require_wc(&wc, &ctx, wc_arg, live, pool));
    SVN_ERR(filees_writer_guard(wc, pool));
    src = svn_dirent_join(wc, old_rel, pool);
    dst = svn_dirent_join(wc, new_rel, pool);
    parent = svn_dirent_dirname(dst, pool);
    SVN_ERR(filees_plain_node(src, APR_REG, TRUE, pool));
    SVN_ERR(filees_plain_node(dst, APR_REG, FALSE, pool));
    SVN_ERR(svn_client_get_wc_root(&root, src, ctx, pool, pool));
    if (strcmp(root, wc)) return filees_refuse("source belongs to another WC");
    SVN_ERR(svn_client_get_wc_root(&root, parent, ctx, pool, pool));
    if (strcmp(root, wc)) return filees_refuse("destination belongs to another WC");
    SVN_ERR(read_status(&s, src, ctx, pool));
    SVN_ERR(read_status(&d, dst, ctx, pool));
    SVN_ERR(read_status(&p, parent, ctx, pool));
    if (live && s->node_status == svn_wc_status_deleted && d->node_status == svn_wc_status_added &&
        !s->conflicted && !d->conflicted && !s->wc_is_locked && !d->wc_is_locked &&
        s->moved_to_abspath && d->moved_from_abspath &&
        !strcmp(s->moved_to_abspath, dst) && !strcmp(d->moved_from_abspath, src)) {
        *state = "already_scheduled";
        return SVN_NO_ERROR;
    }
    if (!s->versioned || s->kind != svn_node_file || s->node_status != svn_wc_status_missing ||
        !SVN_IS_VALID_REVNUM(s->revision) || s->copied || s->conflicted || s->switched ||
        s->file_external || s->wc_is_locked || s->moved_from_abspath || s->moved_to_abspath)
        return filees_refuse("source is not a plain missing committed file");
    if (d->versioned || d->node_status != svn_wc_status_unversioned || d->conflicted)
        return filees_refuse("destination is not an unversioned regular file");
    if (!p->versioned || p->kind != svn_node_dir || p->copied || p->switched ||
        p->conflicted || p->wc_is_locked ||
        (p->node_status != svn_wc_status_normal && p->node_status != svn_wc_status_modified &&
         p->node_status != svn_wc_status_added))
        return filees_refuse("destination parent is not a plain versioned directory");
    SVN_ERR(svn_wc_prop_get2(&special, ctx->wc_ctx, src, SVN_PROP_SPECIAL, pool, pool));
    if (special) return filees_refuse("source metadata describes a special file, not a regular file");
    sources = apr_array_make(pool, 1, sizeof(const char *));
    APR_ARRAY_PUSH(sources, const char *) = src;
    return svn_client_move7(sources, dst, FALSE, FALSE, FALSE, TRUE,
                            NULL, NULL, NULL, ctx, pool);
}

/* The one race a quiet period before scheduling cannot close: the owner moves
 * a renamed file on again between scheduling and the commit that carries it.
 * The scheduled pair (source deleted and moved to the destination, the
 * destination moved here and now missing) is reverted in metadata only:
 * nothing is restored to disk and nothing is asked of the server. The source
 * returns to a plain missing committed file, so the daemon can schedule the
 * whole chain again as one move. Anything else is refused untouched. */
svn_error_t *filees_unschedule_move(const char *wc_arg, const char *old_rel,
                                    const char *new_rel, svn_boolean_t live,
                                    const char **state, apr_pool_t *pool)
{
    const char *wc, *root, *src, *dst;
    svn_client_ctx_t *ctx;
    svn_client_status_t *s, *d;
    apr_array_header_t *targets;
    apr_finfo_t finfo;
    if (!filees_safe_relative(old_rel) || !filees_safe_relative(new_rel) || !strcmp(old_rel, new_rel))
        return filees_refuse("expected two distinct canonical relative data paths");
    SVN_ERR(filees_require_wc(&wc, &ctx, wc_arg, live, pool));
    SVN_ERR(filees_writer_guard(wc, pool));
    src = svn_dirent_join(wc, old_rel, pool);
    dst = svn_dirent_join(wc, new_rel, pool);
    /* Both paths must be absent from disk: this never reverts over a file. */
    if (apr_stat(&finfo, src, APR_FINFO_LINK, pool) == APR_SUCCESS ||
        apr_stat(&finfo, dst, APR_FINFO_LINK, pool) == APR_SUCCESS)
        return filees_refuse("a path of the scheduled move still exists on disk");
    SVN_ERR(svn_client_get_wc_root(&root, src, ctx, pool, pool));
    if (strcmp(root, wc)) return filees_refuse("source belongs to another WC");
    SVN_ERR(read_status(&s, src, ctx, pool));
    if (s->versioned && s->kind == svn_node_file && s->node_status == svn_wc_status_missing &&
        SVN_IS_VALID_REVNUM(s->revision) && !s->copied && !s->conflicted &&
        !s->moved_from_abspath && !s->moved_to_abspath) {
        /* Retried after a lost reply: already undone. */
        *state = "already_unscheduled";
        return SVN_NO_ERROR;
    }
    SVN_ERR(read_status(&d, dst, ctx, pool));
    if (!s->versioned || s->kind != svn_node_file || s->node_status != svn_wc_status_deleted ||
        s->conflicted || s->wc_is_locked || !s->moved_to_abspath || strcmp(s->moved_to_abspath, dst))
        return filees_refuse("source is not deleted and moved to the destination");
    /* SVN reports neither moved-from nor copied for a missing node; the
     * source's moved-to above is the authoritative half of the pair. */
    if (!d->versioned || d->node_status != svn_wc_status_missing ||
        d->conflicted || d->wc_is_locked ||
        (d->moved_from_abspath && strcmp(d->moved_from_abspath, src)))
        return filees_refuse("destination is not a missing node moved here from the source");
    targets = apr_array_make(pool, 2, sizeof(const char *));
    APR_ARRAY_PUSH(targets, const char *) = dst;
    APR_ARRAY_PUSH(targets, const char *) = src;
    SVN_ERR(svn_client_revert4(targets, svn_depth_empty, NULL, FALSE, TRUE, TRUE, ctx, pool));
    *state = "unscheduled";
    return SVN_NO_ERROR;
}
