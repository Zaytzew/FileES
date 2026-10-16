/* Token-bound unlock. The repository checks exactly the supplied token; never
 * substitute a newly observed one. WC write lock spans local proof, RPC and
 * metadata/readonly cleanup. Receipts are emitted only after cleanup succeeds.
 * These two libsvn_wc ABI functions are the same pinned 1.14 entry points used
 * by svn_client_unlock; no direct WC database edits or force unlock. */
#include "filees_svn.h"
#include <stdio.h>
#include <string.h>
#include <apr_hash.h>
#include <apr_strings.h>
#include <svn_dirent_uri.h>
#include <svn_path.h>
#include <svn_ra.h>

svn_error_t *svn_wc__acquire_write_lock(const char **, svn_wc_context_t *,
    const char *, svn_boolean_t, apr_pool_t *, apr_pool_t *);
svn_error_t *svn_wc__release_write_lock(svn_wc_context_t *, const char *, apr_pool_t *);

struct release_info { const char *url, *root, *uuid, *token; apr_pool_t *pool; int count; };
static svn_error_t *release_info_cb(void *baton, const char *path,
    const svn_client_info2_t *info, apr_pool_t *pool)
{
    struct release_info *b = baton;
    (void)path; (void)pool;
    b->count++;
    b->url = info->URL ? apr_pstrdup(b->pool, info->URL) : NULL;
    b->root = info->repos_root_URL ? apr_pstrdup(b->pool, info->repos_root_URL) : NULL;
    b->uuid = info->repos_UUID ? apr_pstrdup(b->pool, info->repos_UUID) : NULL;
    b->token = info->lock && info->lock->token ? apr_pstrdup(b->pool, info->lock->token) : NULL;
    return SVN_NO_ERROR;
}
struct release_result { int count; svn_error_t *error; const char *expected; };
static svn_error_t *release_cb(void *baton, const char *path, svn_boolean_t locking,
    const svn_lock_t *lock, svn_error_t *error, apr_pool_t *pool)
{
    struct release_result *b = baton;
    (void)lock; (void)pool;
    b->count++;
    if (locking || b->count != 1 || !path || strcmp(path, b->expected))
        return filees_refuse("invalid unlock notification");
    if (error) b->error = svn_error_dup(error);
    return SVN_NO_ERROR;
}
static svn_error_t *release_locked(svn_client_ctx_t *ctx, const char *path,
    const char *token, const char **state, apr_pool_t *pool)
{
    struct release_info info = {0};
    struct release_result result = {0};
    svn_ra_session_t *session;
    const char *root, *uuid, *relative;
    apr_hash_t *tokens = apr_hash_make(pool);
    svn_error_t *err;
    info.pool = pool;
    SVN_ERR(svn_client_info4(path, NULL, NULL, svn_depth_empty, FALSE, TRUE,
        FALSE, NULL, release_info_cb, &info, ctx, pool));
    if (info.count != 1 || !info.url || !info.root || !info.uuid ||
        (info.token && strcmp(info.token, token)))
        return filees_refuse("local unlock binding changed");
    SVN_ERR(filees_ra_target(&root, info.root, pool));
    relative = svn_uri_skip_ancestor(root, info.url, pool);
    if (!relative || !*relative) return filees_refuse("unlock target outside repository");
    SVN_ERR(svn_client_open_ra_session2(&session, root, NULL, ctx, pool, pool));
    SVN_ERR(svn_ra_get_uuid2(session, &uuid, pool));
    if (strcmp(uuid, info.uuid)) return filees_refuse("unlock repository incarnation changed");
    apr_hash_set(tokens, relative, APR_HASH_KEY_STRING, token);
    result.expected = relative;
    err = svn_ra_unlock(session, tokens, FALSE, release_cb, &result, pool);
    err = svn_error_compose_create(err, result.error);
    if (err) {
        svn_lock_t *current;
        svn_error_t *read_err = svn_ra_get_lock(session, &current, relative, pool);
        if (read_err) return svn_error_compose_create(err, read_err);
        if (current && current->token && !strcmp(current->token, token)) return err;
        /* Lost reply or already replaced: prove only absence of THIS token.
         * The newer token is never passed to an unlock call. */
        svn_error_clear(err);
        *state = "not-held";
    } else {
        if (result.count != 1) return filees_refuse("missing unlock acknowledgement");
        *state = "released";
    }
    return svn_wc_remove_lock2(ctx->wc_ctx, path, pool);
}
svn_error_t *filees_ra_unlock_token(const char *wc_arg, svn_boolean_t live,
    const char *rel, const char *token, apr_pool_t *pool)
{
    const char *wc, *lock_root, *state = NULL;
    svn_client_ctx_t *ctx;
    apr_array_header_t *paths;
    svn_error_t *err;
    SVN_ERR(filees_require_wc(&wc, &ctx, wc_arg, live, pool));
    SVN_ERR(filees_ra_ctx_auth(ctx, pool));
    SVN_ERR(filees_abs_paths(&paths, wc, &rel, 1, pool));
    SVN_ERR(filees_writer_guard(wc, pool));
    SVN_ERR(svn_wc__acquire_write_lock(&lock_root, ctx->wc_ctx, wc, FALSE, pool, pool));
    err = release_locked(ctx, APR_ARRAY_IDX(paths, 0, const char *), token, &state, pool);
    err = svn_error_compose_create(err, svn_wc__release_write_lock(ctx->wc_ctx, lock_root, pool));
    SVN_ERR(err);
    printf("{\"schema\":\"" FILEES_SVN_SCHEMA "\",\"ok\":true,\"released_lock\":{\"path\":");
    filees_json_string(rel);
    printf(",\"token\":"); filees_json_string(token);
    printf(",\"state\":"); filees_json_string(state);
    puts("}}");
    return SVN_NO_ERROR;
}
