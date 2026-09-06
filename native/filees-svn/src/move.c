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
