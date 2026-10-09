/* URL-only move or explicit single-file delete. No WC, hydration or filesystem
 * fallback. The caller owns the durable intent and must inspect an uncertain
 * result, never blindly replay it. Public RA editor API, not private mtcc ABI. */
#include "filees_svn.h"
#include <stdio.h>
#include <string.h>
#include <apr_strings.h>
#include <svn_delta.h>
#include <svn_dirent_uri.h>
#include <svn_path.h>
#include <svn_props.h>
#include <svn_ra.h>
#include <svn_string.h>

struct tree_edit {
    const svn_delta_editor_t *editor;
    const char *source, *destination, *copy_url;
    svn_revnum_t base, committed;
    svn_node_kind_t kind;
};

static svn_error_t *tree_committed(const svn_commit_info_t *info, void *baton,
                                  apr_pool_t *pool)
{
    struct tree_edit *edit = baton;
    (void)pool;
    edit->committed = info->revision;
    return SVN_NO_ERROR;
}

static int tree_path(const char *path)
{
    const unsigned char *p = (const unsigned char *)path;
    int segments = 1;
    if (!path || strlen(path) > 4096 || !filees_safe_relative(path)) return 0;
    for (; *p; ++p) {
        if (*p < 32 || *p == 127) return 0;
        if (*p == '/' && ++segments > 64) return 0;
    }
    return svn_relpath_is_canonical(path);
}

static int inside(const char *parent, const char *child)
{
    size_t n = strlen(parent);
    return !strncmp(parent, child, n) && child[n] == '/';
}

static int operation_uuid(const char *value)
{
    size_t i;
    int nonzero = 0;
    if (!value || strlen(value) != 36) return 0;
    for (i = 0; i < 36; ++i) {
        char c = value[i];
        if (i == 8 || i == 13 || i == 18 || i == 23) { if (c != '-') return 0; }
        else {
            if (!((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f'))) return 0;
            if (c != '0') nonzero = 1;
        }
    }
    return nonzero;
}

/* Walk just the parents of two named edits, each opened once. The editor must
 * be driven depth-first; reopening a common parent is not a valid shortcut. */
static svn_error_t *tree_directory(struct tree_edit *edit, const char *dir,
                                  void *baton, apr_pool_t *pool)
{
    const char *targets[2] = {edit->source, edit->destination};
    const char *children[2] = {NULL, NULL};
    int i, count = 0;
    for (i = 0; i < 2; ++i) {
        const char *relative;
        const char *slash;
        if (!targets[i]) continue; /* A delete has no destination edit. */
        relative = *dir ? svn_relpath_skip_ancestor(dir, targets[i]) : targets[i];
        if (!relative || !*relative) continue;
        slash = strchr(relative, '/');
        if (slash) {
            const char *child = svn_relpath_join(dir, apr_pstrmemdup(pool, relative, slash - relative), pool);
            if (!count || strcmp(children[0], child)) children[count++] = child;
            continue;
        }
        if (i == 0) {
            SVN_ERR(edit->editor->delete_entry(edit->source, edit->base, baton, pool));
        } else {
            void *node;
            if (edit->kind == svn_node_dir) {
                SVN_ERR(edit->editor->add_directory(edit->destination, baton, edit->copy_url,
                                                    edit->base, pool, &node));
                SVN_ERR(edit->editor->close_directory(node, pool));
            } else {
                SVN_ERR(edit->editor->add_file(edit->destination, baton, edit->copy_url,
                                               edit->base, pool, &node));
                SVN_ERR(edit->editor->close_file(node, NULL, pool));
            }
        }
    }
    for (i = 0; i < count; ++i) {
        void *child;
        SVN_ERR(edit->editor->open_directory(children[i], baton, edit->base, pool, &child));
        SVN_ERR(tree_directory(edit, children[i], child, pool));
        SVN_ERR(edit->editor->close_directory(child, pool));
    }
    return SVN_NO_ERROR;
}

static svn_error_t *tree_begin(struct tree_edit *edit, svn_ra_session_t **session,
                              const char **root, const char *root_arg,
                              const char *uuid, const char *source, const char *destination,
                              svn_revnum_t base, const char *marker, const char *message,
                              apr_pool_t *pool)
{
    svn_client_ctx_t *ctx;
    const char *actual_root, *actual_uuid;
    svn_revnum_t head;
    svn_node_kind_t kind;
    if (!SVN_IS_VALID_REVNUM(base) || !operation_uuid(uuid) || !operation_uuid(marker) ||
        !message || !*message || strlen(message) > 4096 || !tree_path(source) ||
        (destination && (!tree_path(destination) || !strcmp(source, destination) ||
                         inside(source, destination) || inside(destination, source))))
        return filees_refuse("invalid bound remote tree mutation");
    SVN_ERR(filees_ra_target(root, root_arg, pool));
    if (strcmp(*root, root_arg)) return filees_refuse("remote tree root URL must be canonical");
    SVN_ERR(filees_ra_ctx(&ctx, pool));
    SVN_ERR(svn_client_open_ra_session2(session, *root, NULL, ctx, pool, pool));
    SVN_ERR(svn_ra_get_repos_root2(*session, &actual_root, pool));
    SVN_ERR(svn_ra_get_uuid2(*session, &actual_uuid, pool));
    if (strcmp(actual_root, *root) || strcmp(actual_uuid, uuid))
        return filees_refuse("remote tree repository binding changed");
    SVN_ERR(svn_ra_get_latest_revnum(*session, &head, pool));
    if (head != base) return filees_refuse("remote tree revision changed before publication");
    SVN_ERR(svn_ra_check_path(*session, source, base, &edit->kind, pool));
    if (edit->kind != svn_node_file && edit->kind != svn_node_dir)
        return filees_refuse("remote tree source is absent");
    if (destination) {
        SVN_ERR(svn_ra_check_path(*session, destination, base, &kind, pool));
        if (kind != svn_node_none) return filees_refuse("remote tree destination already exists");
        SVN_ERR(svn_ra_check_path(*session, svn_relpath_dirname(destination, pool), base, &kind, pool));
        if (kind != svn_node_dir) return filees_refuse("remote tree destination parent is absent");
    } else if (edit->kind != svn_node_file) {
        return filees_refuse("explicit remote file delete cannot delete a directory");
    }
    edit->source = source; edit->destination = destination; edit->base = base;
    edit->copy_url = svn_path_url_add_component2(*root, source, pool);
    edit->committed = SVN_INVALID_REVNUM;
    return SVN_NO_ERROR;
}

/* Shared editor lifecycle, but separate validated entry points and receipts.
 * An error from get_commit_editor or close_edit has no not-sent guarantee. */
static svn_error_t *tree_publish(struct tree_edit *edit, svn_ra_session_t *session,
                                 const char *marker, const char *message, apr_pool_t *pool)
{
    void *editor_baton, *root_baton;
    apr_hash_t *props = apr_hash_make(pool);
    svn_error_t *err;
    apr_hash_set(props, SVN_PROP_REVISION_LOG, APR_HASH_KEY_STRING, svn_string_create(message, pool));
    apr_hash_set(props, "filees:commit-id", APR_HASH_KEY_STRING, svn_string_create(marker, pool));
    SVN_ERR(svn_ra_get_commit_editor3(session, &edit->editor, &editor_baton, props,
                                      tree_committed, edit, NULL, TRUE, pool));
    err = edit->editor->open_root(editor_baton, edit->base, pool, &root_baton);
    if (!err) err = tree_directory(edit, "", root_baton, pool);
    if (!err) err = edit->editor->close_directory(root_baton, pool);
    if (!err) err = edit->editor->close_edit(editor_baton, pool);
    if (err) {
        svn_error_clear(edit->editor->abort_edit(editor_baton, pool));
        return err;
    }
    if (!SVN_IS_VALID_REVNUM(edit->committed) || edit->committed <= edit->base)
        return filees_refuse("remote mutation has no confirmed revision");
    return SVN_NO_ERROR;
}

/* The explicit file-only API cannot be invoked via a malformed move. Its
 * caller must have a durable, completed OS deletion intent before publication.
 * The RA editor enforces repository locks/authz; it never reads file bytes. */
svn_error_t *filees_remote_file_delete(const char *root_arg, const char *uuid,
                                      svn_revnum_t base, const char *marker,
                                      const char *message, const char *source,
                                      apr_pool_t *pool)
{
    struct tree_edit edit;
    svn_ra_session_t *session;
    const char *root;
    svn_error_t *err = tree_begin(&edit, &session, &root, root_arg, uuid, source,
                                  NULL, base, marker, message, pool);
    if (err) return svn_error_create(err->apr_err, err, FILEES_COMMIT_NOT_SENT);
    SVN_ERR(tree_publish(&edit, session, marker, message, pool));
    printf("{\"schema\":\"" FILEES_SVN_SCHEMA "\",\"ok\":true,\"mutation\":\"delete-file\",\"revision\":%ld,\"base_revision\":%ld,\"uuid\":",
           (long)edit.committed, (long)base);
    filees_json_string(uuid);
    printf(",\"root_url\":"); filees_json_string(root);
    printf(",\"operation_id\":"); filees_json_string(marker);
    printf(",\"source\":"); filees_json_string(source);
    printf(",\"message\":"); filees_json_string(message);
    puts("}");
    return SVN_NO_ERROR;
}

svn_error_t *filees_remote_tree_move(const char *root_arg, const char *uuid,
                                    svn_revnum_t base, const char *marker,
                                    const char *message, const char *source,
                                    const char *destination, apr_pool_t *pool)
{
    struct tree_edit edit;
    svn_ra_session_t *session;
    const char *root;
    svn_error_t *err = tree_begin(&edit, &session, &root, root_arg, uuid, source,
                                  destination, base, marker, message, pool);
    /* This prefix is proof of no editor/transaction, not a licence to retry an
     * error from get_commit_editor or close_edit, whose outcome may be unknown. */
    if (err) return svn_error_create(err->apr_err, err, FILEES_COMMIT_NOT_SENT);
    SVN_ERR(tree_publish(&edit, session, marker, message, pool));
    printf("{\"schema\":\"" FILEES_SVN_SCHEMA "\",\"ok\":true,\"revision\":%ld,\"base_revision\":%ld,\"uuid\":",
           (long)edit.committed, (long)base);
    filees_json_string(uuid);
    printf(",\"root_url\":"); filees_json_string(root);
    printf(",\"operation_id\":"); filees_json_string(marker);
    printf(",\"source\":"); filees_json_string(source);
    printf(",\"destination\":"); filees_json_string(destination);
    puts("}");
    return SVN_NO_ERROR;
}
