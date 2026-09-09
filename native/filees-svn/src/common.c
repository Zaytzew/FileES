#include "filees_svn.h"

#include <ctype.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include <apr_file_info.h>
#include <svn_dirent_uri.h>
#include <svn_error_codes.h>
#include <svn_pools.h>

svn_error_t *filees_refuse(const char *message)
{
    return svn_error_create(SVN_ERR_INCORRECT_PARAMS, NULL, message);
}

void filees_json_string(const char *s)
{
    const unsigned char *p = (const unsigned char *)(s ? s : "");
    putchar('"');
    for (; *p; ++p) {
        if (*p == '"' || *p == '\\') printf("\\%c", *p);
        else if (*p < 32) printf("\\u%04x", (unsigned int)*p);
        else putchar(*p);
    }
    putchar('"');
}

int filees_failure(svn_error_t *err)
{
    svn_error_t *e;
    char buffer[512];
    int first = 1;
    printf("{\"schema\":\"" FILEES_SVN_SCHEMA "\",\"ok\":false,\"errors\":[");
    for (e = err; e; e = e->child) {
        if (!first) putchar(',');
        first = 0;
        printf("{\"code\":%ld,\"message\":", (long)e->apr_err);
        filees_json_string(e->message ? e->message
                                      : svn_strerror(e->apr_err, buffer, sizeof(buffer)));
        putchar('}');
    }
    puts("]}");
    svn_error_clear(err);
    return EXIT_FAILURE;
}

static int same_ascii(const char *a, size_t n, const char *b)
{
    size_t i;
    if (strlen(b) != n) return 0;
    for (i = 0; i < n; ++i)
        if (tolower((unsigned char)a[i]) != tolower((unsigned char)b[i])) return 0;
    return 1;
}

int filees_safe_relative(const char *path)
{
    const char *p = path, *end;
    if (!*p || *p == '/' || strchr(p, '\\') || strchr(p, ':')) return 0;
    do {
        size_t n;
        end = strchr(p, '/');
        n = end ? (size_t)(end - p) : strlen(p);
        if (!n || same_ascii(p, n, ".") || same_ascii(p, n, "..") ||
            same_ascii(p, n, ".svn") || same_ascii(p, n, ".filees") ||
            same_ascii(p, n, FILEES_SVN_MARKER)) return 0;
        if (p[n - 1] == '.' || p[n - 1] == ' ') return 0;
        p = end ? end + 1 : NULL;
    } while (p);
    return 1;
}

const char *filees_status_kind(enum svn_wc_status_kind kind)
{
    switch (kind) {
    case svn_wc_status_none: return "none";
    case svn_wc_status_unversioned: return "unversioned";
    case svn_wc_status_normal: return "normal";
    case svn_wc_status_added: return "added";
    case svn_wc_status_missing: return "missing";
    case svn_wc_status_deleted: return "deleted";
    case svn_wc_status_replaced: return "replaced";
    case svn_wc_status_modified: return "modified";
    case svn_wc_status_merged: return "merged";
    case svn_wc_status_conflicted: return "conflicted";
    case svn_wc_status_ignored: return "ignored";
    case svn_wc_status_obstructed: return "obstructed";
    case svn_wc_status_external: return "external";
    case svn_wc_status_incomplete: return "incomplete";
    default: return "none";
    }
}

static svn_error_t *plain_node(const char *path, apr_filetype_e wanted,
                               svn_boolean_t missing, apr_pool_t *pool)
{
    const char *p = path;
    svn_boolean_t leaf = TRUE;
    while (*p) {
        apr_finfo_t info;
        apr_status_t status = apr_stat(&info, p, APR_FINFO_TYPE | APR_FINFO_LINK, pool);
        if (leaf && missing) {
            if (!APR_STATUS_IS_ENOENT(status))
                return filees_refuse("source must already be absent from disk");
        } else if (missing && !leaf && APR_STATUS_IS_ENOENT(status)) {
            /* Missing source ancestors need no reconstruction. */
        } else {
            if (status) return svn_error_wrap_apr(status, "cannot inspect fixture path");
            if (info.filetype != (leaf ? wanted : APR_DIR))
                return filees_refuse("non-regular node or symlink in fixture path");
        }
        {
            const char *parent = svn_dirent_dirname(p, pool);
            if (!strcmp(parent, p)) break;
            p = parent;
        }
        leaf = FALSE;
    }
    return SVN_NO_ERROR;
}

svn_error_t *filees_inspect_wc(const char **wc_abspath, svn_client_ctx_t **ctx,
                               const char *wc_arg, apr_pool_t *pool)
{
    const char *wc, *root;
    if (!wc_arg || !*wc_arg || strstr(wc_arg, "://"))
        return filees_refuse("repository URLs are not accepted");
    SVN_ERR(svn_dirent_get_absolute(&wc, svn_dirent_internal_style(wc_arg, pool), pool));
    SVN_ERR(plain_node(wc, APR_DIR, FALSE, pool));
    SVN_ERR(svn_client_create_context2(ctx, NULL, pool));
    SVN_ERR(svn_client_get_wc_root(&root, wc, *ctx, pool, pool));
    if (strcmp(root, wc)) return filees_refuse("working copy argument must name the exact WC root");
    *wc_abspath = wc;
    return SVN_NO_ERROR;
}

svn_error_t *filees_require_wc(const char **wc_abspath, svn_client_ctx_t **ctx,
                               const char *wc_arg, svn_boolean_t live,
                               apr_pool_t *pool)
{
    SVN_ERR(filees_inspect_wc(wc_abspath, ctx, wc_arg, pool));
    return plain_node(svn_dirent_join(*wc_abspath, live ? ".filees" : FILEES_SVN_MARKER, pool),
                      live ? APR_DIR : APR_REG, FALSE, pool);
}

svn_error_t *filees_relpath(const char **rel, const char *wc,
                            const char *abspath, apr_pool_t *pool)
{
    const char *skip = svn_dirent_skip_ancestor(wc, abspath);
    if (!skip)
        /* Naming both sides: "path is outside the working copy" without them
         * is true and useless, and this codebase has paid for that shape of
         * message more than once. */
        return filees_refuse(apr_psprintf(pool, "path %s is outside the working copy %s",
                                          abspath, wc));
    *rel = *skip ? skip : ".";
    return SVN_NO_ERROR;
}

svn_error_t *filees_abs_paths(apr_array_header_t **out, const char *wc,
                              const char **rels, int nrels, apr_pool_t *pool)
{
    int i;
    if (nrels < 1) return filees_refuse("expected one or more relative paths");
    if (nrels > FILEES_SVN_MAX_TARGETS) return filees_refuse("too many paths");
    *out = apr_array_make(pool, nrels, sizeof(const char *));
    for (i = 0; i < nrels; ++i) {
        const char *abs;
        if (!filees_safe_relative(rels[i]))
            return filees_refuse("expected canonical relative data paths");
        abs = svn_dirent_join(wc, rels[i], pool);
        APR_ARRAY_PUSH(*out, const char *) = abs;
    }
    return SVN_NO_ERROR;
}

svn_error_t *filees_plain_node(const char *path, apr_filetype_e wanted,
                               svn_boolean_t missing, apr_pool_t *pool)
{
    return plain_node(path, wanted, missing, pool);
}

/* Shared by info and log: one spelling of a node kind, so two verbs cannot
 * disagree about what a directory is called. */
const char *filees_node_kind(svn_node_kind_t kind)
{
    switch (kind) {
    case svn_node_file: return "file";
    case svn_node_dir: return "dir";
    case svn_node_none: return "none";
    default: return "unknown";
    }
}
