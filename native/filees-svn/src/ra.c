/* Remote verbs. Everything here talks to a server; nothing here needs a
 * working copy, which makes it the first part of the helper outside the
 * .filees marker guard - see filees_ra_target for what replaces it. */
#include "filees_svn.h"

#include <stdio.h>
#include <string.h>

#include <apr_file_io.h>
#include <apr_strings.h>
#include <svn_cmdline.h>
#include <svn_config.h>
#include <svn_dirent_uri.h>
#include <svn_io.h>
#include <svn_path.h>

/* filees_ra_ctx builds a context able to reach a server.
 *
 * The config hash stays NULL, exactly as the WC-local verbs leave it. That is
 * not an oversight: measured 2026-09-08 against the production server
 * (reports/NATIVE_SVN_RA_SSH_PROBE_2026-09-08.md), an svn+ssh tunnel opens the
 * same with a NULL config as with the user's, because the built-in [tunnels]
 * ssh entry honours SVN_SSH either way. NULL is the more hermetic of the two:
 * nothing the machine happens to have in ~/.subversion can change how a pinned
 * deployment connects.
 *
 * The auth baton is non-interactive and caches nothing. FileES authenticates
 * with the ssh key the daemon pins; a prompt here would be a hang, and a cache
 * would be credentials this process has no business keeping. */
static svn_error_t *filees_ra_ctx(svn_client_ctx_t **ctx, apr_pool_t *pool)
{
    SVN_ERR(svn_client_create_context2(ctx, NULL, pool));
    SVN_ERR(svn_cmdline_create_auth_baton2(&(*ctx)->auth_baton,
                                           TRUE,  /* non_interactive */
                                           NULL, NULL, NULL,
                                           TRUE,  /* no_auth_cache */
                                           FALSE, FALSE, FALSE, FALSE, FALSE,
                                           NULL, NULL, NULL, pool));
    return SVN_NO_ERROR;
}

/* filees_ra_target validates a repository URL.
 *
 * The WC verbs are guarded by a marker file inside the directory they are about
 * to touch. A remote verb has no such anchor, so the guard here is narrower and
 * different in kind: the argument must be a URL and nothing else. A local path
 * arriving where a URL is expected means the caller is confused, and the one
 * thing this process must never do is start reading the filesystem because an
 * argument was mistyped. */
static svn_error_t *filees_ra_target(const char **canonical, const char *url,
                                     apr_pool_t *pool)
{
    if (!url || !*url) return filees_refuse("missing repository URL");
    if (!svn_path_is_url(url)) return filees_refuse("expected a repository URL");
    return svn_uri_canonicalize_safe(canonical, NULL, url, pool, pool);
}

/* filees_ra_cat writes one repository file to disk.
 *
 * Keywords are deliberately NOT expanded. The CLI expands them by default, and
 * expansion substitutes the URL and revision into the bytes - so the same
 * committed file yields different content depending on where it was fetched
 * from. This verb exists to carry release material that is verified by
 * signature, and a signature over context-dependent bytes verifies nothing.
 *
 * The download lands on a ".part" sibling opened exclusively and is renamed
 * only after the stream closes. A truncated fetch therefore never occupies the
 * final name: an interrupted self-update leaves nothing that looks finished. */
svn_error_t *filees_ra_cat(const char *url_arg, const char *out_path,
                           svn_revnum_t revision, apr_pool_t *pool)
{
    const char *url, *partial;
    svn_client_ctx_t *ctx;
    svn_opt_revision_t peg, rev;
    apr_file_t *file;
    svn_stream_t *stream;
    apr_finfo_t finfo;
    apr_status_t status;

    SVN_ERR(filees_ra_target(&url, url_arg, pool));
    if (!out_path || !*out_path) return filees_refuse("--out must be an absolute path");
    /* SVN dirents are internal style, so a Windows path arrives looking
     * relative until its separators are converted. filees_require_wc does the
     * same to its argument; skipping it here made every absolute Windows
     * target refuse itself. */
    out_path = svn_dirent_internal_style(out_path, pool);
    if (!svn_dirent_is_absolute(out_path))
        return filees_refuse("--out must be an absolute path");
    {
        apr_finfo_t existing;
        if (apr_stat(&existing, out_path, APR_FINFO_TYPE, pool) == APR_SUCCESS)
            return filees_refuse("--out already exists; this verb never overwrites");
    }
    /* Also refuses a symlink anywhere in the parent chain. */
    SVN_ERR(filees_plain_node(out_path, APR_REG, TRUE, pool));

    peg.kind = svn_opt_revision_unspecified;
    if (SVN_IS_VALID_REVNUM(revision)) {
        rev.kind = svn_opt_revision_number;
        rev.value.number = revision;
    } else {
        rev.kind = svn_opt_revision_head;
    }

    partial = apr_pstrcat(pool, out_path, ".part", NULL);
    status = apr_file_open(&file, partial,
                           APR_FOPEN_CREATE | APR_FOPEN_WRITE | APR_FOPEN_EXCL,
                           APR_FPROT_UREAD | APR_FPROT_UWRITE, pool);
    if (status != APR_SUCCESS)
        return svn_error_wrap_apr(status, "cannot create %s", partial);

    stream = svn_stream_from_aprfile2(file, FALSE, pool);
    SVN_ERR(filees_ra_ctx(&ctx, pool));
    {
        /* A failed fetch must leave nothing behind. Without this the .part
         * survives and the next attempt fails on the exclusive open instead
         * of on the real reason - a retry that reports the wrong problem. */
        svn_error_t *err = svn_client_cat3(NULL, stream, url, &peg, &rev, FALSE,
                                           ctx, pool, pool);
        if (!err) err = svn_stream_close(stream);
        else svn_error_clear(svn_stream_close(stream));
        if (err) {
            svn_error_clear(svn_io_remove_file2(partial, TRUE, pool));
            return err;
        }
    }

    if ((status = apr_stat(&finfo, partial, APR_FINFO_SIZE, pool)) != APR_SUCCESS)
        return svn_error_wrap_apr(status, "cannot measure %s", partial);
    SVN_ERR(svn_io_file_rename2(partial, out_path, FALSE, pool));

    printf("{\"schema\":\"" FILEES_SVN_SCHEMA "\",\"ok\":true,\"bytes\":%" APR_OFF_T_FMT,
           finfo.size);
    if (SVN_IS_VALID_REVNUM(revision)) printf(",\"revision\":%ld", (long)revision);
    puts("}");
    return SVN_NO_ERROR;
}
