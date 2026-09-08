/* Disposable probe: can a non-CLI process reach svn+ssh:// through libsvn?
 *
 * FileES does not speak http:// or svn://. Every repository is svn+ssh:// with
 * a forced command on the server, and the daemon pins the tunnel through
 * SVN_SSH (pkg/client/client.go:91) - identity file, known_hosts, port, all
 * absolute and slash-normalised because Subversion unescapes that string
 * before splitting it.
 *
 * The question this answers is narrow and load-bearing: the helper builds its
 * context with svn_client_create_context2(ctx, NULL, pool), so it has NO
 * config, and the ssh tunnel agent is looked up in config [tunnels] - which is
 * where SVN_SSH is honoured. If an empty config means no tunnel, then every RA
 * verb needs config wiring that nothing in the helper does today, and finding
 * that out after writing checkout would be the expensive order.
 *
 * It asks the server for one number and nothing else: no working copy, no
 * write, no lock. Build it with -DFILEES_SVN_BUILD_RA_PROBE=ON; it is off by
 * default so it cannot reach a release by accident.
 */
#include <stdio.h>
#include <string.h>

#include <apr_general.h>
#include <apr_hash.h>
#include <svn_client.h>
#include <svn_cmdline.h>
#include <svn_config.h>
#include <svn_error.h>
#include <svn_hash.h>
#include <svn_pools.h>
#include <svn_ra.h>

#define PROBE_SCHEMA "filees.native-svn-ra-probe/v1"

static void json_string(const char *s)
{
    putchar('"');
    for (; s && *s; ++s) {
        unsigned char c = (unsigned char)*s;
        if (c == '"' || c == '\\') printf("\\%c", c);
        else if (c < 0x20) printf("\\u%04x", c);
        else putchar(c);
    }
    putchar('"');
}

static svn_error_t *probe(const char *url, svn_boolean_t with_config,
                          apr_pool_t *pool)
{
    apr_hash_t *config = NULL;
    svn_config_t *cfg = NULL;
    svn_ra_callbacks2_t *callbacks;
    svn_ra_session_t *session;
    svn_revnum_t rev;

    if (with_config) {
        /* NULL config_dir means the user's default, which is where the
         * built-in [tunnels] ssh entry - and therefore SVN_SSH - lives. */
        SVN_ERR(svn_config_get_config(&config, NULL, pool));
        cfg = svn_hash_gets(config, SVN_CONFIG_CATEGORY_CONFIG);
    }
    SVN_ERR(svn_ra_create_callbacks(&callbacks, pool));
    SVN_ERR(svn_cmdline_create_auth_baton2(&callbacks->auth_baton,
                                           TRUE,  /* non_interactive: never prompt */
                                           NULL, NULL, NULL,
                                           TRUE,  /* no_auth_cache */
                                           FALSE, FALSE, FALSE, FALSE, FALSE,
                                           cfg, NULL, NULL, pool));
    SVN_ERR(svn_ra_open5(&session, NULL, NULL, url, NULL, callbacks, NULL,
                         config, pool));
    SVN_ERR(svn_ra_get_latest_revnum(session, &rev, pool));

    printf("{\"schema\":\"" PROBE_SCHEMA "\",\"ok\":true,\"config\":");
    json_string(with_config ? "default" : "none");
    printf(",\"revision\":%ld}\n", (long)rev);
    return SVN_NO_ERROR;
}

static int failure(svn_error_t *err, svn_boolean_t with_config)
{
    svn_error_t *walk;
    int first = 1;

    printf("{\"schema\":\"" PROBE_SCHEMA "\",\"ok\":false,\"config\":");
    json_string(with_config ? "default" : "none");
    printf(",\"errors\":[");
    for (walk = err; walk; walk = walk->child) {
        char buf[512];
        if (!first) putchar(',');
        first = 0;
        printf("{\"code\":%ld,\"message\":", (long)walk->apr_err);
        json_string(svn_err_best_message(walk, buf, sizeof(buf)));
        putchar('}');
    }
    puts("]}");
    svn_error_clear(err);
    return 1;
}

int main(int argc, const char *argv[])
{
    apr_pool_t *pool;
    svn_boolean_t with_config = TRUE;
    const char *url = NULL;
    svn_error_t *err;
    int i;

    if (svn_cmdline_init("filees-svn-raprobe", stderr) != EXIT_SUCCESS) return 2;
    pool = svn_pool_create(NULL);

    for (i = 1; i < argc; ++i) {
        if (!strcmp(argv[i], "--config") && i + 1 < argc) {
            ++i;
            if (!strcmp(argv[i], "none")) with_config = FALSE;
            else if (!strcmp(argv[i], "default")) with_config = TRUE;
            else { fputs("--config expects none or default\n", stderr); return 2; }
        } else if (!url) {
            url = argv[i];
        } else {
            fputs("one URL only\n", stderr);
            return 2;
        }
    }
    if (!url) {
        fputs("usage: filees-svn-raprobe [--config none|default] URL\n", stderr);
        return 2;
    }

    err = probe(url, with_config, pool);
    if (err) return failure(err, with_config);
    return 0;
}
