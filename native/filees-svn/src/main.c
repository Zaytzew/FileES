/* FileES native SVN client. First verb: local metadata-only file move. */
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <ctype.h>
#include <apr_file_info.h>
#include <apr_general.h>
#include <apr_strings.h>
#include <svn_client.h>
#include <svn_cmdline.h>
#include <svn_dirent_uri.h>
#include <svn_error.h>
#include <svn_pools.h>
#include <svn_props.h>
#include <svn_version.h>
#ifdef _WIN32
#include <windows.h>
#endif

#define MARKER ".filees-native-probe"
#define SCHEMA "filees.native-svn/v1"

static svn_error_t *refuse(const char *message)
{
    return svn_error_create(SVN_ERR_INCORRECT_PARAMS, NULL, message);
}

static void json_string(const char *s)
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

static int failure(svn_error_t *err)
{
    svn_error_t *e;
    char buffer[512];
    int first = 1;
    printf("{\"schema\":\"" SCHEMA "\",\"ok\":false,\"errors\":[");
    for (e = err; e; e = e->child) {
        if (!first) putchar(',');
        first = 0;
        printf("{\"code\":%ld,\"message\":", (long)e->apr_err);
        json_string(e->message ? e->message : svn_strerror(e->apr_err, buffer, sizeof(buffer)));
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

static int safe_relative(const char *path)
{
    const char *p = path, *end;
    if (!*p || *p == '/' || strchr(p, '\\') || strchr(p, ':')) return 0;
    do {
        size_t n;
        end = strchr(p, '/');
        n = end ? (size_t)(end - p) : strlen(p);
        if (!n || same_ascii(p, n, ".") || same_ascii(p, n, "..") ||
            same_ascii(p, n, ".svn") || same_ascii(p, n, ".filees") ||
            same_ascii(p, n, MARKER)) return 0;
        /* Also reject Windows trailing-dot/space aliases on every platform. */
        if (p[n - 1] == '.' || p[n - 1] == ' ') return 0;
        p = end ? end + 1 : NULL;
    } while (p);
    return 1;
}

/* Check all existing ancestors without following symlinks. No race guarantee. */
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
                return refuse("source must already be absent from disk");
        } else if (missing && !leaf && APR_STATUS_IS_ENOENT(status)) {
            /* A file may have moved with its containing directory. Missing
               source ancestors need no reconstruction; existing ancestors
               are still checked, and the exact WC root is checked separately. */
        } else {
            if (status) return svn_error_wrap_apr(status, "cannot inspect fixture path");
            if (info.filetype != (leaf ? wanted : APR_DIR))
                return refuse("non-regular node or symlink in fixture path");
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
    if (!o.status) return refuse("SVN did not describe the exact requested path");
    *status = o.status;
    return SVN_NO_ERROR;
}

static svn_error_t *record_move(const char *wc_arg, const char *old_rel,
                                const char *new_rel, svn_boolean_t live,
                                const char **state, apr_pool_t *pool)
{
    const char *wc, *root, *src, *dst, *parent;
    svn_client_ctx_t *ctx;
    svn_client_status_t *s, *d, *p;
    const svn_string_t *special;
    apr_array_header_t *sources;
    if (!safe_relative(old_rel) || !safe_relative(new_rel) || !strcmp(old_rel, new_rel))
        return refuse("expected two distinct canonical relative data paths");
    if (strstr(wc_arg, "://")) return refuse("repository URLs are not accepted");
    SVN_ERR(svn_dirent_get_absolute(&wc, svn_dirent_internal_style(wc_arg, pool), pool));
    SVN_ERR(plain_node(wc, APR_DIR, FALSE, pool));
    SVN_ERR(plain_node(svn_dirent_join(wc, live ? ".filees" : MARKER, pool),
                       live ? APR_DIR : APR_REG, FALSE, pool));
    SVN_ERR(svn_client_create_context2(&ctx, NULL, pool));
    SVN_ERR(svn_client_get_wc_root(&root, wc, ctx, pool, pool));
    if (strcmp(root, wc)) return refuse("--disposable-wc must name the exact WC root");
    src = svn_dirent_join(wc, old_rel, pool);
    dst = svn_dirent_join(wc, new_rel, pool);
    parent = svn_dirent_dirname(dst, pool);
    SVN_ERR(plain_node(src, APR_REG, TRUE, pool));
    SVN_ERR(plain_node(dst, APR_REG, FALSE, pool));
    SVN_ERR(svn_client_get_wc_root(&root, src, ctx, pool, pool));
    if (strcmp(root, wc)) return refuse("source belongs to another WC");
    SVN_ERR(svn_client_get_wc_root(&root, parent, ctx, pool, pool));
    if (strcmp(root, wc)) return refuse("destination belongs to another WC");
    SVN_ERR(read_status(&s, src, ctx, pool));
    SVN_ERR(read_status(&d, dst, ctx, pool));
    SVN_ERR(read_status(&p, parent, ctx, pool));
    /* Reply lost or daemon restarted after scheduling: only the exact SVN
       move pair proves success. A plain deleted/added pair is NOT enough. */
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
        return refuse("source is not a plain missing committed file");
    if (d->versioned || d->node_status != svn_wc_status_unversioned || d->conflicted)
        return refuse("destination is not an unversioned regular file");
    if (!p->versioned || p->kind != svn_node_dir || p->copied || p->switched ||
        p->conflicted || p->wc_is_locked ||
        (p->node_status != svn_wc_status_normal && p->node_status != svn_wc_status_modified &&
         p->node_status != svn_wc_status_added))
        return refuse("destination parent is not a plain versioned directory");
    SVN_ERR(svn_wc_prop_get2(&special, ctx->wc_ctx, src, SVN_PROP_SPECIAL, pool, pool));
    if (special) return refuse("source metadata describes a special file, not a regular file");
    sources = apr_array_make(pool, 1, sizeof(const char *));
    APR_ARRAY_PUSH(sources, const char *) = src;
    /* The only mutation: no physical move, no mixed-revision downgrade. */
    return svn_client_move7(sources, dst, FALSE, FALSE, FALSE, TRUE,
                            NULL, NULL, NULL, ctx, pool);
}

static int run(int argc, const char **argv)
{
    apr_pool_t *pool;
    svn_error_t *err = NULL;
    int result = EXIT_SUCCESS;
    if (svn_cmdline_init("filees-svn", stderr) != EXIT_SUCCESS) return EXIT_FAILURE;
    pool = svn_pool_create(NULL);
    if (argc == 2 && !strcmp(argv[1], "--version")) {
        const svn_version_t *v = svn_client_version();
        printf("{\"schema\":\"" SCHEMA "\",\"ok\":true,\"svn_runtime\":\"%d.%d.%d\","
               "\"svn_headers\":\"" SVN_VER_NUMBER "\"}\n", v->major, v->minor, v->patch);
    } else if (argc == 6 && !strcmp(argv[1], "record-move") &&
               (!strcmp(argv[2], "--disposable-wc") || !strcmp(argv[2], "--wc"))) {
        const char *wc = argv[3], *old_rel = argv[4], *new_rel = argv[5];
        const char *state = "scheduled";
#ifndef _WIN32
        err = svn_cmdline_cstring_to_utf8(&wc, argv[3], pool);
        if (!err) err = svn_cmdline_cstring_to_utf8(&old_rel, argv[4], pool);
        if (!err) err = svn_cmdline_cstring_to_utf8(&new_rel, argv[5], pool);
#endif
        if (!err) err = record_move(wc, old_rel, new_rel, !strcmp(argv[2], "--wc"), &state, pool);
        if (!err) printf("{\"schema\":\"" SCHEMA "\",\"ok\":true,\"state\":\"%s\"}\n", state);
    } else {
        err = refuse("usage: filees-svn record-move --wc|--disposable-wc WC OLD_REL NEW_REL");
    }
    if (err) result = failure(err);
    svn_pool_destroy(pool);
    apr_terminate();
    return result;
}

#ifdef _WIN32
/* Wide argv avoids the system ANSI code page for Polish/other Unicode paths. */
int wmain(int argc, wchar_t **wide_argv)
{
    int i, result = EXIT_FAILURE;
    const char **argv = calloc((size_t)argc + 1, sizeof(*argv));
    if (!argv) return EXIT_FAILURE;
    for (i = 0; i < argc; ++i) {
        int n = WideCharToMultiByte(CP_UTF8, WC_ERR_INVALID_CHARS, wide_argv[i], -1, NULL, 0, NULL, NULL);
        char *arg;
        if (!n) goto done;
        arg = malloc((size_t)n);
        if (!arg) goto done;
        argv[i] = arg;
        if (!WideCharToMultiByte(CP_UTF8, WC_ERR_INVALID_CHARS, wide_argv[i], -1, arg, n, NULL, NULL)) goto done;
    }
    result = run(argc, argv);
done:
    for (i = 0; i < argc; ++i) free((void *)argv[i]);
    free(argv);
    return result;
}
#else
int main(int argc, char **argv)
{
    return run(argc, (const char **)argv);
}
#endif
