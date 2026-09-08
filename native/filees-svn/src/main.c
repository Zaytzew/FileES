/* FileES native SVN client. WC-local verbs plus metadata-only move. */
#include "filees_svn.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include <apr_general.h>
#include <svn_cmdline.h>
#include <svn_pools.h>
#include <svn_version.h>
#ifdef _WIN32
#include <windows.h>
#endif

static const char *const k_verbs[] = {
    "record-move", "status", "info", "add", "delete", "propget", "propset",
    "propdel", "cleanup", "revert", "resolve", NULL
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
    puts("]}");
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

static svn_error_t *run_verb(int argc, const char **argv, apr_pool_t *pool)
{
    const char *verb, *wc = NULL;
    svn_boolean_t live = TRUE, recursive = FALSE, depth_set = FALSE;
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

    for (i = 2; i < argc; ++i) {
        if (!strcmp(argv[i], "--")) {
            SVN_ERR(collect_paths(i, argc, argv, paths, &n));
            i = argc;
            break;
        }
        if (!strcmp(argv[i], "--wc") || !strcmp(argv[i], "--disposable-wc")) {
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
        SVN_ERR(filees_wc_status(wc, live, paths, n, depth, pool));
        return SVN_NO_ERROR;
    } else if (!strcmp(verb, "info")) {
        SVN_ERR(filees_wc_info(wc, live, paths, n, pool));
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
