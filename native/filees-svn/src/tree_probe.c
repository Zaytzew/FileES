/* Opt-in disposable experiment, not a release helper or a reconciler.
 * Only public SVN APIs, no SQL/private symbols and no RA/remote mutation.
 * The tests establish a real remote copyfrom/delete BEFORE passing its revision.
 * This probe does not validate a receipt or authorize ACK/retirement.
 * A recursive committed queue is deliberately tested with UNSENT local changes:
 * its apparent success may be unsafe. Never expose it as a product operation. */
#include "filees_svn.h"
#include <windows.h>
#include <stdlib.h>
#include <string.h>
#include <stdio.h>
#include <apr_general.h>
#include <apr_strings.h>
#include <svn_cmdline.h>
#include <svn_dirent_uri.h>
#include <svn_pools.h>

static svn_error_t *probe(int argc, const char **argv, apr_pool_t *pool)
{
    const char *wc, *src, *dst;
    svn_client_ctx_t *ctx;
    apr_array_header_t *sources;
    svn_wc_adm_access_t *adm;
    svn_wc_committed_queue_t *queue;
    svn_revnum_t revision;
    const char *end;
    int move;
    if (argc != 6 || (strcmp(argv[1], "metadata") && strcmp(argv[1], "crash-metadata") &&
                      strcmp(argv[1], "ack") && strcmp(argv[1], "ack-roots")))
        return filees_refuse("expected metadata|crash-metadata|ack|ack-roots WC source destination revision");
    if (!filees_safe_relative(argv[3]) || !filees_safe_relative(argv[4]) ||
        !strcmp(argv[3], argv[4]) || svn_relpath_skip_ancestor(argv[3], argv[4]) ||
        svn_relpath_skip_ancestor(argv[4], argv[3]))
        return filees_refuse("expected disjoint relative data paths");
    SVN_ERR(svn_revnum_parse(&revision, argv[5], &end));
    if (*end || revision < 1) return filees_refuse("expected explicit positive fixture revision");
    /* No --wc/live escape hatch: the ordinary disposable marker is required. */
    SVN_ERR(filees_require_wc(&wc, &ctx, argv[2], FALSE, pool));
    src = svn_dirent_join(wc, argv[3], pool);
    dst = svn_dirent_join(wc, argv[4], pool);
    SVN_ERR(filees_plain_node(src, APR_DIR, TRUE, pool));
    SVN_ERR(filees_plain_node(dst, APR_DIR, FALSE, pool));
    move = strcmp(argv[1], "ack") && strcmp(argv[1], "ack-roots");
    if (move) {
        sources = apr_array_make(pool, 1, sizeof(const char *));
        APR_ARRAY_PUSH(sources, const char *) = src;
        SVN_ERR(svn_client_move7(sources, dst, FALSE, FALSE, TRUE, TRUE,
                                NULL, NULL, NULL, ctx, pool));
        /* Cut after the library returns and releases its WC locks, before ACK. */
        if (!strcmp(argv[1], "crash-metadata")) ExitProcess(77);
    } else {
        /* Public compatibility APIs provide the matching owned write lock.
         * The modern queue API alone has no public write-lock acquisition API.
         * These are still public library functions, not private entry points. */
        SVN_ERR(svn_wc_adm_open3(&adm, NULL, wc, TRUE, -1, NULL, NULL, pool));
        queue = svn_wc_committed_queue_create(pool);
        svn_boolean_t recurse = !strcmp(argv[1], "ack");
        SVN_ERR(svn_wc_queue_committed2(queue, src, adm, recurse, NULL, FALSE, FALSE, NULL, pool));
        SVN_ERR(svn_wc_queue_committed2(queue, dst, adm, recurse, NULL, FALSE, FALSE, NULL, pool));
        SVN_ERR(svn_wc_process_committed_queue(queue, adm, revision, NULL, "fixture", pool));
        SVN_ERR(svn_wc_adm_close2(adm, pool));
    }
    puts("{\"schema\":\"filees.tree-probe/v1\",\"ok\":true}");
    return SVN_NO_ERROR;
}

int wmain(int argc, WCHAR **wide)
{
    apr_pool_t *pool;
    const char **argv;
    svn_error_t *err = NULL;
    int i, result;
    if (svn_cmdline_init("filees-tree-probe", stderr)) return 2;
    /* Measure rejected queue states as errors, never launch WER/core dialogs. */
    svn_error_set_malfunction_handler(svn_error_raise_on_malfunction);
    pool = svn_pool_create(NULL);
    argv = apr_pcalloc(pool, ((apr_size_t)argc + 1) * sizeof(*argv));
    for (i = 0; i < argc; ++i) {
        int n = WideCharToMultiByte(CP_UTF8, WC_ERR_INVALID_CHARS, wide[i], -1, NULL, 0, NULL, NULL);
        char *arg;
        if (!n) { err = filees_refuse("invalid Unicode fixture argument"); break; }
        arg = apr_palloc(pool, n);
        if (!WideCharToMultiByte(CP_UTF8, WC_ERR_INVALID_CHARS, wide[i], -1, arg, n, NULL, NULL)) {
            err = filees_refuse("invalid Unicode fixture argument"); break;
        }
        argv[i] = arg;
    }
    if (!err) err = probe(argc, argv, pool);
    result = err ? filees_failure(err) : 0;
    svn_pool_destroy(pool);
    apr_terminate();
    return result;
}
