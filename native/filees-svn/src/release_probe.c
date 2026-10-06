/* Opt-in disposable local probe, never staged into a runtime/release.
 * Interrupt the private operation after the BASE transaction and before
 * dispatching its durable work item. No SQL, RA, GUI or production files. */
#include <windows.h>
#include <stdio.h>
#include <string.h>
#include <apr_general.h>
#include <svn_client.h>
#include <svn_cmdline.h>
#include <svn_dirent_uri.h>
#include <svn_pools.h>

typedef svn_error_t *(*release_fn)(svn_wc_context_t *, const char *, const char *,
    void *, svn_cancel_func_t, void *, apr_pool_t *);
struct cancel_state { svn_client_ctx_t *ctx; const char *path; int crash; apr_pool_t *pool; };

static svn_error_t *after_transition(void *baton)
{
    struct cancel_state *s = baton;
    svn_node_kind_t kind;
    SVN_ERR(svn_wc_read_kind2(&kind, s->ctx->wc_ctx, s->path, FALSE, FALSE, s->pool));
    if (kind != svn_node_none) return SVN_NO_ERROR;
    if (s->crash) ExitProcess(77);
    return svn_error_create(SVN_ERR_CANCELLED, NULL, "disposable probe: deferred verified removal");
}

int wmain(int argc, WCHAR **argv)
{
    apr_pool_t *pool;
    svn_client_ctx_t *ctx;
    svn_error_t *err;
    release_fn fn;
    HANDLE source;
    struct cancel_state state;
    char *path, *retained;
    int n;
    if (argc != 4 || (wcscmp(argv[3], L"defer") && wcscmp(argv[3], L"crash"))) return 2;
    if (svn_cmdline_init("filees-release-probe", stderr)) return 3;
    pool = svn_pool_create(NULL);
    n = WideCharToMultiByte(CP_UTF8, WC_ERR_INVALID_CHARS, argv[1], -1, NULL, 0, NULL, NULL);
    path = apr_palloc(pool, n);
    if (!n || !WideCharToMultiByte(CP_UTF8, WC_ERR_INVALID_CHARS, argv[1], -1, path, n, NULL, NULL)) return 4;
    n = WideCharToMultiByte(CP_UTF8, WC_ERR_INVALID_CHARS, argv[2], -1, NULL, 0, NULL, NULL);
    retained = apr_palloc(pool, n);
    if (!n || !WideCharToMultiByte(CP_UTF8, WC_ERR_INVALID_CHARS, argv[2], -1, retained, n, NULL, NULL)) return 4;
    path = (char *)svn_dirent_internal_style(path, pool);
    retained = (char *)svn_dirent_internal_style(retained, pool);
    err = svn_client_create_context2(&ctx, NULL, pool);
    if (err) { svn_error_clear(err); return 5; }
    fn = (release_fn)GetProcAddress(GetModuleHandleW(L"libsvn_wc-1.dll"), "svn_wc__filees_exclude_retained_v1");
    if (!fn) return 6;
    source = CreateFileW(argv[1], GENERIC_READ, FILE_SHARE_READ | FILE_SHARE_DELETE,
                         NULL, OPEN_EXISTING, FILE_FLAG_OPEN_REPARSE_POINT, NULL);
    if (source == INVALID_HANDLE_VALUE) return 7;
    state.ctx = ctx; state.path = path; state.crash = !wcscmp(argv[3], L"crash"); state.pool = pool;
    err = fn(ctx->wc_ctx, path, retained, source, after_transition, &state, pool);
    CloseHandle(source);
    if (err) { svn_handle_error2(err, stderr, FALSE, "probe: "); svn_error_clear(err); }
    svn_pool_destroy(pool);
    apr_terminate();
    return err ? 1 : 0;
}
