/* FileES native SVN client. WC-local verbs plus metadata-only move. */
#include "filees_svn.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include <apr_general.h>
#include <apr_hash.h>
#include <svn_cmdline.h>
#include <svn_dirent_uri.h>
#include <svn_pools.h>
#include <svn_version.h>
#ifdef _WIN32
#include <windows.h>
#include <fcntl.h>
#include <io.h>
#endif

static const char *const k_verbs[] = {
    "record-move", "checkout", "update", "commit", "lock", "unlock", "cat",
    "log", "status", "info", "add", "delete", "propget", "propset",
    "propdel", "cleanup", "revert", "resolve", "recover-commit", NULL
};

static void print_ok_version(void)
{
    const svn_version_t *v = svn_client_version();
    int i;
    printf("{\"schema\":\"" FILEES_SVN_SCHEMA "\",\"ok\":true,\"svn_runtime\":\"%d.%d.%d\","
           "\"svn_headers\":\"" SVN_VER_NUMBER "\",\"verbs\":[",
           v->major, v->minor, v->patch);
    for (i = 0; k_verbs[i]; ++i) {
        if (i) putchar(',');
        filees_json_string(k_verbs[i]);
    }
    puts("],\"features\":[\"update_changes\",\"commit_targets_stdin_v1\",\"info_inspect_remote_v1\",\"status_remote_locks_v1\",\"recover_plain_add_v1\",\"writer_lease_v1\",\"sparse_checkout_v1\"]}");
}

/* Stdin is UTF-8 on every platform, independent of the process locale. */
static int target_utf8(const unsigned char *p)
{
    while (*p) {
        unsigned int code, low;
        int left;
        if (*p < 128) { if (*p < 32 || *p == 127) return 0; ++p; continue; }
        if (*p >= 0xc2 && *p <= 0xdf) { code = *p & 31; left = 1; low = 0x80; }
        else if (*p >= 0xe0 && *p <= 0xef) { code = *p & 15; left = 2; low = 0x800; }
        else if (*p >= 0xf0 && *p <= 0xf4) { code = *p & 7; left = 3; low = 0x10000; }
        else return 0;
        ++p;
        while (left--) {
            if ((*p & 0xc0) != 0x80) return 0;
            code = (code << 6) | (*p++ & 63);
        }
        if (code < low || code > 0x10ffff || (code >= 0xd800 && code <= 0xdfff)) return 0;
    }
    return 1;
}

/* Complete validation before opening the WC or contacting the repository.
 * Every target ends in NUL, including the last; EOF mid-target is an error.
 * No response file races, temporary paths, quoting, or command-line limit. */
static svn_error_t *stdin_targets(const char ***paths, int *n, apr_pool_t *pool)
{
    char *buf = apr_palloc(pool, FILEES_SVN_TARGET_BYTES + 1);
    size_t bytes, off = 0;
    apr_array_header_t *list = apr_array_make(pool, 1024, sizeof(const char *));
    apr_hash_t *seen = apr_hash_make(pool);
#ifdef _WIN32
    if (_setmode(_fileno(stdin), _O_BINARY) == -1) return filees_refuse("cannot read binary targets");
#endif
    bytes = fread(buf, 1, FILEES_SVN_TARGET_BYTES + 1, stdin);
    if (ferror(stdin)) return filees_refuse("cannot read targets");
    if (!bytes || bytes > FILEES_SVN_TARGET_BYTES || buf[bytes - 1] != '\0')
        return filees_refuse("empty, oversized or truncated targets");
    while (off < bytes) {
        const char *path = buf + off;
        if (list->nelts >= FILEES_SVN_MAX_TARGETS) return filees_refuse("too many targets");
        if (!target_utf8((const unsigned char *)path) || !filees_safe_relative(path))
            return filees_refuse("invalid UTF-8 relative target");
        if (apr_hash_get(seen, path, APR_HASH_KEY_STRING)) return filees_refuse("duplicate target");
        apr_hash_set(seen, path, APR_HASH_KEY_STRING, path);
        APR_ARRAY_PUSH(list, const char *) = path;
        off += strlen(path) + 1;
    }
    *paths = (const char **)list->elts;
    *n = list->nelts;
    return SVN_NO_ERROR;
}

static svn_error_t *parse_wc_flag(int *i, int argc, const char **argv,
                                  const char **wc, svn_boolean_t *live)
{
    if (*i + 1 >= argc) return filees_refuse("missing working copy path");
    if (!strcmp(argv[*i], "--wc")) *live = TRUE;
    else if (!strcmp(argv[*i], "--disposable-wc")) *live = FALSE;
    else return filees_refuse("expected --wc or --disposable-wc");
    *wc = argv[++*i];
    return SVN_NO_ERROR;
}

static svn_error_t *collect_paths(int i, int argc, const char **argv,
                                  const char **paths, int *n)
{
    if (i < argc && !strcmp(argv[i], "--")) ++i;
    *n = 0;
    for (; i < argc; ++i) {
        if (*n >= FILEES_SVN_MAX_PATHS) return filees_refuse("too many paths");
        paths[(*n)++] = argv[i];
    }
    return SVN_NO_ERROR;
}

static svn_error_t *parse_one_revision(const char *text, svn_opt_revision_t *rev)
{
    char *end;
    long value;
    if (!strcmp(text, "HEAD")) {
        rev->kind = svn_opt_revision_head;
        return SVN_NO_ERROR;
    }
    value = strtol(text, &end, 10);
    if (*end || value < 0) return filees_refuse("revision must be HEAD or a non-negative number");
    rev->kind = svn_opt_revision_number;
    rev->value.number = (svn_revnum_t)value;
    return SVN_NO_ERROR;
}

/* "N" means that revision alone, "A:B" a range. A bare number is the common
 * case and must not silently become "everything up to N". */
static svn_error_t *parse_revision_range(const char *text, apr_pool_t *pool,
                                         svn_opt_revision_t *start,
                                         svn_opt_revision_t *end)
{
    const char *colon = strchr(text, ':');
    if (!colon) {
        SVN_ERR(parse_one_revision(text, start));
        *end = *start;
        return SVN_NO_ERROR;
    }
    SVN_ERR(parse_one_revision(apr_pstrndup(pool, text, (apr_size_t)(colon - text)), start));
    return parse_one_revision(colon + 1, end);
}

#define FILEES_LOG_MAX_REVPROPS 8

static svn_error_t *run_log(int argc, const char **argv, apr_pool_t *pool)
{
    const char *url = NULL, *wc = NULL, *target = NULL;
    const char *revprops[FILEES_LOG_MAX_REVPROPS];
    int nrevprops = 0, limit = 0, i;
    svn_boolean_t live = TRUE, changed = FALSE, have_range = FALSE, peg_base = FALSE;
    svn_opt_revision_t peg, start, end;
    svn_client_ctx_t *ctx;

    for (i = 2; i < argc; ++i) {
        if (!strcmp(argv[i], "--url") && i + 1 < argc) { url = argv[++i]; continue; }
        if (!strcmp(argv[i], "--wc") || !strcmp(argv[i], "--disposable-wc")) {
            SVN_ERR(parse_wc_flag(&i, argc, argv, &wc, &live));
            continue;
        }
        if (!strcmp(argv[i], "--revision") && i + 1 < argc) {
            SVN_ERR(parse_revision_range(argv[++i], pool, &start, &end));
            have_range = TRUE;
            continue;
        }
        if (!strcmp(argv[i], "--limit") && i + 1 < argc) {
            char *stop;
            long value = strtol(argv[++i], &stop, 10);
            if (*stop || value < 1 || value > 100000) return filees_refuse("--limit must be 1..100000");
            limit = (int)value;
            continue;
        }
        if (!strcmp(argv[i], "--changed-paths")) { changed = TRUE; continue; }
        if (!strcmp(argv[i], "--peg-base")) { peg_base = TRUE; continue; }
        if (!strcmp(argv[i], "--revprop") && i + 1 < argc) {
            if (nrevprops >= FILEES_LOG_MAX_REVPROPS) return filees_refuse("too many --revprop");
            revprops[nrevprops++] = argv[++i];
            continue;
        }
        if (!strcmp(argv[i], "--")) {
            if (i + 2 < argc) return filees_refuse("log takes one target");
            if (i + 1 < argc) target = argv[++i];
            continue;
        }
        return filees_refuse("usage: filees-svn log (--url URL | --wc WC -- REL) --revision A[:B] "
                             "[--limit N] [--changed-paths] [--peg-base] [--revprop NAME]");
    }

    if ((url == NULL) == (wc == NULL))
        return filees_refuse("log needs exactly one of --url and --wc");
    if (!have_range) return filees_refuse("log requires --revision");

    peg.kind = peg_base ? svn_opt_revision_base : svn_opt_revision_unspecified;
    if (url) {
        if (target) return filees_refuse("--url takes no separate target");
        SVN_ERR(filees_ra_target(&target, url, pool));
        SVN_ERR(filees_ra_ctx(&ctx, pool));
    } else {
        const char *root;
        if (!target) return filees_refuse("--wc requires one relative target after --");
        if (!filees_safe_relative(target)) return filees_refuse("target must be relative and inside the working copy");
        SVN_ERR(filees_require_wc(&root, &ctx, wc, live, pool));
        /* The working copy verbs run offline; log does not, and cannot. It is
         * a repository question asked about a local path, so the context needs
         * credentials even though the target is on disk. */
        SVN_ERR(filees_ra_ctx_auth(ctx, pool));
        target = svn_dirent_join(root, target, pool);
    }
    return filees_log(ctx, target, &peg, &start, &end, limit, changed,
                      revprops, nrevprops, pool);
}

static svn_error_t *parse_revision_flag(int *i, int argc, const char **argv,
                                        svn_revnum_t *revision)
{
    char *end;
    long value;
    if (*i + 1 >= argc) return filees_refuse("missing --revision value");
    value = strtol(argv[++*i], &end, 10);
    if (*end || value < 0) return filees_refuse("--revision must be a non-negative number");
    *revision = (svn_revnum_t)value;
    return SVN_NO_ERROR;
}

static svn_error_t *run_checkout(int argc, const char **argv, apr_pool_t *pool)
{
    const char *url = NULL, *wc = NULL;
    svn_revnum_t revision = SVN_INVALID_REVNUM;
    svn_boolean_t force = FALSE;
    svn_depth_t depth = svn_depth_infinity;
    int i;

    for (i = 2; i < argc; ++i) {
        if (!strcmp(argv[i], "--url") && i + 1 < argc) { url = argv[++i]; continue; }
        if (!strcmp(argv[i], "--wc") && i + 1 < argc) { wc = argv[++i]; continue; }
        if (!strcmp(argv[i], "--force")) { force = TRUE; continue; }
        if (!strcmp(argv[i], "--depth") && i + 1 < argc) {
            const char *value = argv[++i];
            if (!strcmp(value, "empty")) depth = svn_depth_empty;
            else if (!strcmp(value, "infinity")) depth = svn_depth_infinity;
            else return filees_refuse("checkout --depth must be empty or infinity");
            continue;
        }
        if (!strcmp(argv[i], "--revision")) {
            SVN_ERR(parse_revision_flag(&i, argc, argv, &revision));
            continue;
        }
        return filees_refuse("usage: filees-svn checkout --url URL --wc PATH [--revision N] [--force] [--depth empty|infinity]");
    }
    if (!url || !wc) return filees_refuse("checkout requires --url and --wc");
    return filees_ra_checkout(url, wc, revision, force, depth, pool);
}

static svn_error_t *run_update(int argc, const char **argv, apr_pool_t *pool)
{
    const char *wc = NULL;
    const char *paths[FILEES_SVN_MAX_PATHS];
    svn_revnum_t revision = SVN_INVALID_REVNUM;
    svn_depth_t depth = svn_depth_unknown;
    svn_boolean_t live = TRUE;
    int n = 0, i;

    for (i = 2; i < argc; ++i) {
        if (!strcmp(argv[i], "--wc") || !strcmp(argv[i], "--disposable-wc")) {
            SVN_ERR(parse_wc_flag(&i, argc, argv, &wc, &live));
            continue;
        }
        if (!strcmp(argv[i], "--revision")) {
            SVN_ERR(parse_revision_flag(&i, argc, argv, &revision));
            continue;
        }
        if (!strcmp(argv[i], "--depth") && i + 1 < argc) {
            ++i;
            if (!strcmp(argv[i], "empty")) depth = svn_depth_empty;
            else if (!strcmp(argv[i], "infinity")) depth = svn_depth_infinity;
            else return filees_refuse("--depth must be empty or infinity");
            continue;
        }
        if (!strcmp(argv[i], "--")) {
            SVN_ERR(collect_paths(i, argc, argv, paths, &n));
            break;
        }
        return filees_refuse("usage: filees-svn update --wc WC [--depth empty] [--revision N] [-- REL...]");
    }
    if (!wc) return filees_refuse("update requires --wc|--disposable-wc");
    return filees_ra_update(wc, live, paths, n, depth, revision, pool);
}

static svn_error_t *run_commit(int argc, const char **argv, apr_pool_t *pool)
{
    const char *wc = NULL, *message = NULL;
    const char *argv_paths[FILEES_SVN_MAX_PATHS], **paths = argv_paths;
    const char *revprops[FILEES_LOG_MAX_REVPROPS];
    int n = 0, nrevprops = 0, i;
    svn_boolean_t live = TRUE, keep_locks = FALSE, from_stdin = FALSE;

    for (i = 2; i < argc; ++i) {
        if (!strcmp(argv[i], "--wc") || !strcmp(argv[i], "--disposable-wc")) {
            SVN_ERR(parse_wc_flag(&i, argc, argv, &wc, &live));
            continue;
        }
        if (!strcmp(argv[i], "-m") && i + 1 < argc) { message = argv[++i]; continue; }
        if (!strcmp(argv[i], "--keep-locks")) { keep_locks = TRUE; continue; }
        if (!strcmp(argv[i], "--targets-stdin")) {
            if (from_stdin) return filees_refuse("duplicate --targets-stdin");
            from_stdin = TRUE;
            continue;
        }
        if (!strcmp(argv[i], "--revprop") && i + 1 < argc) {
            if (nrevprops >= FILEES_LOG_MAX_REVPROPS) return filees_refuse("too many --revprop");
            revprops[nrevprops++] = argv[++i];
            continue;
        }
        if (!strcmp(argv[i], "--")) {
            if (from_stdin) return filees_refuse("stdin and argv targets are mutually exclusive");
            SVN_ERR(collect_paths(i, argc, argv, paths, &n));
            break;
        }
        return filees_refuse("usage: filees-svn commit --wc WC -m MESSAGE [--keep-locks] "
                             "[--revprop NAME=VALUE] -- REL...");
    }
    if (!wc) return filees_refuse("commit requires --wc|--disposable-wc");
    if (from_stdin) SVN_ERR(stdin_targets(&paths, &n, pool));
    return filees_ra_commit(wc, live, paths, n, message, keep_locks, revprops, nrevprops, pool);
}

static svn_error_t *run_recover_commit(int argc, const char **argv, apr_pool_t *pool)
{
    const char *wc = NULL, *url = NULL, *marker = NULL;
    const char **paths = NULL;
    svn_revnum_t revision = SVN_INVALID_REVNUM;
    svn_boolean_t live = TRUE, input = FALSE;
    int i, n = 0;
    for (i = 2; i < argc; ++i) {
        if (!strcmp(argv[i], "--wc") || !strcmp(argv[i], "--disposable-wc")) {
            SVN_ERR(parse_wc_flag(&i, argc, argv, &wc, &live)); continue;
        }
        if (!strcmp(argv[i], "--url") && i + 1 < argc) { url = argv[++i]; continue; }
        if (!strcmp(argv[i], "--commit-id") && i + 1 < argc) { marker = argv[++i]; continue; }
        if (!strcmp(argv[i], "--revision")) { SVN_ERR(parse_revision_flag(&i, argc, argv, &revision)); continue; }
        if (!strcmp(argv[i], "--targets-stdin") && !input) { input = TRUE; continue; }
        return filees_refuse("invalid recover-commit arguments");
    }
    if (!wc || !url || !marker || !*marker || revision < 1 || !input)
        return filees_refuse("recover-commit requires WC, URL, commit-id, revision and targets-stdin");
    SVN_ERR(stdin_targets(&paths, &n, pool));
    return filees_recover_commit(wc, live, url, marker, revision, paths, n, pool);
}

static svn_error_t *run_lock(int argc, const char **argv, svn_boolean_t locking,
                             apr_pool_t *pool)
{
    const char *wc = NULL, *comment = NULL;
    const char *paths[FILEES_SVN_MAX_PATHS];
    int n = 0, i;
    svn_boolean_t live = TRUE;

    for (i = 2; i < argc; ++i) {
        if (!strcmp(argv[i], "--wc") || !strcmp(argv[i], "--disposable-wc")) {
            SVN_ERR(parse_wc_flag(&i, argc, argv, &wc, &live));
            continue;
        }
        if (locking && !strcmp(argv[i], "-m") && i + 1 < argc) { comment = argv[++i]; continue; }
        if (!strcmp(argv[i], "--")) {
            SVN_ERR(collect_paths(i, argc, argv, paths, &n));
            break;
        }
        /* --steal and --break are absent on purpose; see filees_ra_lock. */
        return filees_refuse(locking
                                 ? "usage: filees-svn lock --wc WC [-m COMMENT] -- REL..."
                                 : "usage: filees-svn unlock --wc WC -- REL...");
    }
    if (!wc) return filees_refuse("requires --wc|--disposable-wc");
    if (locking) return filees_ra_lock(wc, live, paths, n, comment, pool);
    return filees_ra_unlock(wc, live, paths, n, pool);
}

static svn_error_t *run_info(int argc, const char **argv, apr_pool_t *pool)
{
    const char *wc = NULL, *url = NULL;
    const char *paths[FILEES_SVN_MAX_PATHS];
    int i, n = 0;
    svn_boolean_t live = TRUE, inspect = FALSE;
    for (i = 2; i < argc; ++i) {
        if (!strcmp(argv[i], "--")) { SVN_ERR(collect_paths(i, argc, argv, paths, &n)); break; }
        if (!strcmp(argv[i], "--url") && i + 1 < argc && !wc && !url) { url = argv[++i]; continue; }
        if (!strcmp(argv[i], "--inspect-wc") && i + 1 < argc && !wc && !url) {
            wc = argv[++i]; inspect = TRUE; continue;
        }
        if ((!strcmp(argv[i], "--wc") || !strcmp(argv[i], "--disposable-wc")) && !wc && !url) {
            SVN_ERR(parse_wc_flag(&i, argc, argv, &wc, &live)); continue;
        }
        return filees_refuse("info requires one --url, --inspect-wc, --wc or --disposable-wc target");
    }
    return filees_info(url, wc, live, inspect, paths, n, pool);
}

static svn_error_t *run_verb(int argc, const char **argv, apr_pool_t *pool)
{
    const char *verb, *wc = NULL;
    svn_boolean_t live = TRUE, recursive = FALSE, depth_set = FALSE, remote = FALSE, inspect = FALSE;
    svn_depth_t depth = svn_depth_empty;
    const char *accept = NULL, *propname = NULL, *propval = NULL;
    const char *paths[FILEES_SVN_MAX_PATHS];
    int n = 0, i;
    svn_wc_conflict_choice_t choice;

    if (argc < 2) return filees_refuse("usage: filees-svn VERB --wc WC [args]");
    verb = argv[1];
    if (!strcmp(verb, "--version") || !strcmp(verb, "verbs")) {
        print_ok_version();
        return SVN_NO_ERROR;
    }
    if (!strcmp(verb, "record-move")) {
        const char *state = "scheduled";
        if (argc != 6) return filees_refuse("usage: filees-svn record-move --wc|--disposable-wc WC OLD_REL NEW_REL");
        if (!strcmp(argv[2], "--wc")) live = TRUE;
        else if (!strcmp(argv[2], "--disposable-wc")) live = FALSE;
        else return filees_refuse("usage: filees-svn record-move --wc|--disposable-wc WC OLD_REL NEW_REL");
        SVN_ERR(filees_record_move(argv[3], argv[4], argv[5], live, &state, pool));
        printf("{\"schema\":\"" FILEES_SVN_SCHEMA "\",\"ok\":true,\"state\":\"%s\"}\n", state);
        return SVN_NO_ERROR;
    }

    if (!strcmp(verb, "commit")) return run_commit(argc, argv, pool);
    if (!strcmp(verb, "lock")) return run_lock(argc, argv, TRUE, pool);
    if (!strcmp(verb, "unlock")) return run_lock(argc, argv, FALSE, pool);
    if (!strcmp(verb, "checkout")) return run_checkout(argc, argv, pool);
    if (!strcmp(verb, "update")) return run_update(argc, argv, pool);
    if (!strcmp(verb, "recover-commit")) return run_recover_commit(argc, argv, pool);
    if (!strcmp(verb, "log")) return run_log(argc, argv, pool);
    if (!strcmp(verb, "info")) return run_info(argc, argv, pool);

    if (!strcmp(verb, "cat")) {
        /* Handled before the shared flag loop: cat is the first verb with no
         * working copy, so the --wc requirement below does not apply to it. */
        const char *url = NULL, *out = NULL;
        svn_revnum_t revision = SVN_INVALID_REVNUM;
        for (i = 2; i < argc; ++i) {
            if (!strcmp(argv[i], "--url") && i + 1 < argc) { url = argv[++i]; continue; }
            if (!strcmp(argv[i], "--out") && i + 1 < argc) { out = argv[++i]; continue; }
            if (!strcmp(argv[i], "--revision") && i + 1 < argc) {
                char *end;
                long value = strtol(argv[++i], &end, 10);
                if (*end || value < 0) return filees_refuse("--revision must be a non-negative number");
                revision = (svn_revnum_t)value;
                continue;
            }
            return filees_refuse("usage: filees-svn cat --url URL --out PATH [--revision N]");
        }
        SVN_ERR(filees_ra_cat(url, out, revision, pool));
        return SVN_NO_ERROR;
    }

    for (i = 2; i < argc; ++i) {
        if (!strcmp(verb, "status") && !strcmp(argv[i], "--show-updates")) { remote = TRUE; continue; }
        if (!strcmp(verb, "status") && !strcmp(argv[i], "--inspect-wc") && !wc && i + 1 < argc) {
            wc = argv[++i]; inspect = TRUE; continue;
        }
        if (!strcmp(argv[i], "--")) {
            SVN_ERR(collect_paths(i, argc, argv, paths, &n));
            i = argc;
            break;
        }
        if (!strcmp(argv[i], "--wc") || !strcmp(argv[i], "--disposable-wc")) {
            if (wc) return filees_refuse("duplicate working-copy target");
            SVN_ERR(parse_wc_flag(&i, argc, argv, &wc, &live));
            continue;
        }
        if (!strcmp(argv[i], "--depth")) {
            if (i + 1 >= argc) return filees_refuse("missing --depth value");
            ++i;
            if (!strcmp(argv[i], "empty")) depth = svn_depth_empty;
            else if (!strcmp(argv[i], "infinity")) depth = svn_depth_infinity;
            else return filees_refuse("--depth must be empty or infinity");
            depth_set = TRUE;
            continue;
        }
        if (!strcmp(argv[i], "--recursive")) {
            recursive = TRUE;
            continue;
        }
        if (!strcmp(argv[i], "--accept")) {
            if (i + 1 >= argc) return filees_refuse("missing --accept value");
            accept = argv[++i];
            continue;
        }
        if (argv[i][0] == '-') return filees_refuse("unknown flag");
        if (!strcmp(verb, "propset") && !propname) {
            propname = argv[i];
            if (i + 1 >= argc) return filees_refuse("propset requires NAME VALUE");
            propval = argv[++i];
            continue;
        }
        if ((!strcmp(verb, "propget") || !strcmp(verb, "propdel")) && !propname) {
            propname = argv[i];
            continue;
        }
        SVN_ERR(collect_paths(i, argc, argv, paths, &n));
        break;
    }
    if (!wc) return filees_refuse("missing --wc|--disposable-wc");

    if (!strcmp(verb, "add")) {
        SVN_ERR(filees_wc_add(wc, live, paths, n, pool));
    } else if (!strcmp(verb, "delete")) {
        SVN_ERR(filees_wc_delete(wc, live, paths, n, pool));
    } else if (!strcmp(verb, "status")) {
        if (!depth_set) depth = (n == 0) ? svn_depth_infinity : svn_depth_empty;
        SVN_ERR(filees_wc_status(wc, live, paths, n, depth, remote, inspect, pool));
        return SVN_NO_ERROR;
    } else if (!strcmp(verb, "propset")) {
        SVN_ERR(filees_wc_propset(wc, live, propname, propval, paths, n, pool));
    } else if (!strcmp(verb, "propdel")) {
        SVN_ERR(filees_wc_propdel(wc, live, propname, paths, n, pool));
    } else if (!strcmp(verb, "propget")) {
        SVN_ERR(filees_wc_propget(wc, live, propname, paths, n, recursive, pool));
        return SVN_NO_ERROR;
    } else if (!strcmp(verb, "cleanup")) {
        if (n) return filees_refuse("cleanup takes no paths");
        SVN_ERR(filees_wc_cleanup(wc, live, pool));
    } else if (!strcmp(verb, "revert")) {
        SVN_ERR(filees_wc_revert(wc, live, paths, n, pool));
    } else if (!strcmp(verb, "resolve")) {
        if (!accept) return filees_refuse("resolve requires --accept");
        if (!strcmp(accept, "theirs-full")) choice = svn_wc_conflict_choose_theirs_full;
        else if (!strcmp(accept, "mine-full")) choice = svn_wc_conflict_choose_mine_full;
        else return filees_refuse("--accept must be theirs-full or mine-full");
        SVN_ERR(filees_wc_resolve(wc, live, paths, n, choice, pool));
    } else {
        return filees_refuse("unknown verb");
    }
    printf("{\"schema\":\"" FILEES_SVN_SCHEMA "\",\"ok\":true}\n");
    return SVN_NO_ERROR;
}

static int run(int argc, const char **argv)
{
    apr_pool_t *pool;
    svn_error_t *err = NULL;
    int result = EXIT_SUCCESS;
    if (svn_cmdline_init("filees-svn", stderr) != EXIT_SUCCESS) return EXIT_FAILURE;
    pool = svn_pool_create(NULL);
#ifdef _WIN32
    /* Own the tunnel before any verb can spawn it. The unnamed handle is
     * non-inheritable and deliberately lives until process teardown (including
     * TerminateProcess from Go's context cancellation). Its last close kills
     * SSH and descendants, releasing inherited receipt pipes. Never close it
     * explicitly while this process still has JSON/stdio to flush. */
    {
        HANDLE job = CreateJobObjectW(NULL, NULL);
        JOBOBJECT_EXTENDED_LIMIT_INFORMATION limits = {0};
        limits.BasicLimitInformation.LimitFlags = JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE;
        if (!job || !SetInformationJobObject(job, JobObjectExtendedLimitInformation,
                                            &limits, sizeof(limits)) ||
            !AssignProcessToJobObject(job, GetCurrentProcess())) {
            DWORD code = GetLastError();
            if (job) CloseHandle(job);
            err = svn_error_createf(APR_FROM_OS_ERROR(code), NULL,
                                    "cannot own native SVN process tree (Windows %lu)",
                                    (unsigned long)code);
        }
    }
#endif
#ifndef _WIN32
    /* Declared inside the guard: on Windows wmain already hands us UTF-8, so
     * an unconditional declaration was unused there and warned on every /W4
     * build. Warning noise is where real warnings go to hide. */
    int i;
    for (i = 1; i < argc && !err; ++i) {
        const char *utf8;
        err = svn_cmdline_cstring_to_utf8(&utf8, argv[i], pool);
        if (!err) argv[i] = utf8;
    }
#endif
    if (!err) err = run_verb(argc, argv, pool);
    if (err) result = filees_failure(err);
    svn_pool_destroy(pool);
    apr_terminate();
    return result;
}

#ifdef _WIN32
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
