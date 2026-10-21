/* FileES Explorer anchor helper: argv in, one JSON object out.
 *
 * Verbs:
 *   version
 *   register   --root <anchor> --identity <server id \0 repo id, as text>
 *   shell-register --root <anchor> --identity <text> --icon <absolute .ico> [--visible 0|1]
 *   shell-visibility --root <anchor> --visible 0|1
 *   shell-hide-obsolete --root <missing former anchor> --registration-id <owned ID>
 *   unregister --root <anchor>
 *   info       --root <anchor>
 *   file-state --root <anchor> --rel <file>                (metadata only)
 *   file-states --root <anchor>                           (up to 64 paths on stdin)
 *   hydrate    --root <anchor> --rel <file>                (explicit one-file download)
 *   placeholders --root <anchor> [--rel <subdirectory>]   (listing on stdin)
 *   connect    --root <anchor>                            (holds until EOF)
 *   revert     --root <materialized file>                 (placeholder -> ordinary file)
 */
#include "filees_cfapi.h"

#include <stdio.h>
#include <errno.h>
#include <stdlib.h>
#include <string.h>

static const char *const k_verbs[] = {"version", "register", "shell-register", "shell-visibility", "shell-hide-obsolete", "unregister", "info", "registration-snapshot", "file-state", "file-states", "tree-states", "hydrate", "verify-download", "placeholders", "connect", "revert", NULL};

static void print_version(void)
{
    int i;
    printf("{\"schema\":\"" FILEES_CFAPI_SCHEMA "\",\"ok\":true,\"version\":\"" FILEES_CFAPI_VERSION "\",\"verbs\":[");
    for (i = 0; k_verbs[i]; ++i) {
        if (i) putchar(',');
        filees_cfapi_json_string(k_verbs[i]);
    }
    /* Features are how the daemon decides what this build can do, exactly as
     * with the native SVN helper: a name appears here only when the thing
     * behind it works. */
    printf("],\"features\":[\"sync_root_v1\",\"shell_sync_root_v1\",\"placeholders_v1\",\"refuse_delete_rename_v1\",\"fetch_bridge_v1\",\"revert_placeholder_v1\",\"local_state_usn_v1\",\"local_states_batch_v1\",\"batch_release_observation_v1\",\"explicit_hydrate_v1\",\"prepare_release_usn_v1\",\"restore_release_v1\",\"release_observation_v1\",\"prepare_release_bound_v1\",\"prepare_release_verified_v1\",\"remote_tree_v1\",\"bound_tree_move_v1\",\"directory_state_bound_v1\"");
#ifdef FILEES_CFAPI_DELETE_PROBE
    printf(",\"delete_callback_probe_v1\",\"delete_bridge_probe_v1\"");
#endif
    printf(",\"workqueue_projection_bound_v1\"");
    printf(",\"file_delete_bound_v1\",\"explorer_tree_move_v1\",\"shell_hide_obsolete_v1\",\"shell_visibility_v1\",\"shell_namespace_visibility_v1\",\"registration_evidence_v1\",\"remote_stream_state_usn_v1\",\"fetch_range_receipt_v1\",\"download_integrity_v1\"");
    puts("]}");
    fflush(stdout);
}

static const char *option(int argc, char **argv, const char *name)
{
    int i;
    for (i = 2; i + 1 < argc; i += 2) {
        if (!strcmp(argv[i], name)) return argv[i + 1];
    }
    return NULL;
}

/* An anchor path is always absolute and always the daemon's own choice; a
 * relative one would resolve against this process's directory, which is not a
 * place anybody picked. */
static int absolute_path(const WCHAR *path)
{
    if (!path || !path[0]) return 0;
    if (path[0] == L'\\' && path[1] == L'\\') return 1;
    return ((path[0] >= L'A' && path[0] <= L'Z') || (path[0] >= L'a' && path[0] <= L'z')) &&
           path[1] == L':' && (path[2] == L'\\' || path[2] == L'/');
}

static int run(int argc, char **argv)
{
    WCHAR root[MAX_PATH * 2];
    WCHAR identity[FILEES_CFAPI_MAX_IDENTITY];
    WCHAR relative[FILEES_CFAPI_MAX_PATH];
    WCHAR icon[FILEES_CFAPI_MAX_PATH];
    const char *verb, *value;

    if (argc < 2) {
        filees_cfapi_fail("verb_missing", E_INVALIDARG);
        return 2;
    }
    verb = argv[1];
    if (!strcmp(verb, "version")) {
        print_version();
        return 0;
    }

    value = option(argc, argv, "--root");
    if (!value || !filees_cfapi_widen(value, root, MAX_PATH * 2) || !absolute_path(root)) {
        filees_cfapi_fail("root_invalid", E_INVALIDARG);
        return 2;
    }

    if (!strcmp(verb, "register") || !strcmp(verb, "shell-register")) {
        value = option(argc, argv, "--identity");
        if (!value || !*value || !filees_cfapi_widen(value, identity, FILEES_CFAPI_MAX_IDENTITY)) {
            filees_cfapi_fail("identity_invalid", E_INVALIDARG);
            return 2;
        }
        if (!strcmp(verb, "shell-register")) {
            value = option(argc, argv, "--icon");
            if (!value || !filees_cfapi_widen(value, icon, FILEES_CFAPI_MAX_PATH) || !absolute_path(icon)) {
                filees_cfapi_fail("icon_invalid", E_INVALIDARG);
                return 2;
            }
            value = option(argc, argv, "--visible");
            if (value && strcmp(value,"0") && strcmp(value,"1")) {
                filees_cfapi_fail("shell_visibility_invalid", E_INVALIDARG);
                return 2;
            }
            // No navigation shortcut without explicit opt-in, including callers
            // predating the separate per-WC setting.
            return filees_cfapi_shell_register(root, identity, icon, value && !strcmp(value,"1"));
        }
        return filees_cfapi_register(root, identity);
    }
    if (!strcmp(verb, "shell-visibility")) {
        value = option(argc, argv, "--visible");
        if (!value || (strcmp(value,"0") && strcmp(value,"1"))) {
            filees_cfapi_fail("shell_visibility_invalid", E_INVALIDARG);
            return 2;
        }
        return filees_cfapi_shell_visibility(root, !strcmp(value,"1"));
    }
    if (!strcmp(verb, "shell-hide-obsolete")) {
        value = option(argc, argv, "--registration-id");
        if (!value || !*value || !filees_cfapi_widen(value, identity, FILEES_CFAPI_MAX_IDENTITY)) {
            filees_cfapi_fail("shell_registration_id_invalid", E_INVALIDARG);
            return 2;
        }
        return filees_cfapi_shell_hide_obsolete(root, identity);
    }
    if (!strcmp(verb, "unregister")) return filees_cfapi_unregister(root);
    if (!strcmp(verb, "registration-snapshot")) {
        value = option(argc, argv, "--phase");
        if (!value || (strcmp(value,"move_before") && strcmp(value,"move_after") && strcmp(value,"rollback"))) {
            filees_cfapi_fail("diagnostic_phase_invalid", E_INVALIDARG); return 2;
        }
        filees_registration_trace(root,"lifecycle",value,S_OK,NULL);
        filees_cfapi_ok("diagnostic_snapshot"); return 0;
    }
    if (!strcmp(verb, "info")) return filees_cfapi_info(root);
    if (!strcmp(verb, "revert")) return filees_cfapi_revert(root);
    if (!strcmp(verb, "verify-download")) {
        const char *id = option(argc, argv, "--file-id"), *hash = option(argc, argv, "--sha256");
        const char *size_text = option(argc, argv, "--size"), *usn_text = option(argc, argv, "--usn"), *p;
        char *end;
        LONGLONG size, usn;
        value = option(argc, argv, "--rel");
        if (!value || !filees_cfapi_widen(value, relative, FILEES_CFAPI_MAX_PATH)) goto invalid_verify;
        value = option(argc, argv, "--identity");
        if (!value || !filees_cfapi_widen(value, identity, FILEES_CFAPI_MAX_IDENTITY) || !id || !hash ||
            !size_text || !*size_text || !usn_text || !*usn_text) goto invalid_verify;
        for (p = size_text; *p; ++p) if (*p < '0' || *p > '9') goto invalid_verify;
        for (p = usn_text; *p; ++p) if (*p < '0' || *p > '9') goto invalid_verify;
        errno = 0; size = _strtoi64(size_text, &end, 10);
        if (errno || *end || size < 0) goto invalid_verify;
        errno = 0; usn = _strtoi64(usn_text, &end, 10);
        if (errno || *end || usn <= 0) goto invalid_verify;
        return filees_cfapi_verify_download(root, relative, identity, id, size, usn, hash);
invalid_verify:
        filees_cfapi_fail("verify_download_invalid", E_INVALIDARG); return 2;
    }
    if (!strcmp(verb, "file-states")) return filees_cfapi_file_states(root);
    if (!strcmp(verb, "tree-states")) return filees_cfapi_tree_states(root);
    if (!strcmp(verb, "file-state") || !strcmp(verb, "hydrate")) {
        value = option(argc, argv, "--rel");
        if (!value || !*value || !filees_cfapi_widen(value, relative, FILEES_CFAPI_MAX_PATH)) {
            filees_cfapi_fail("relative_invalid", E_INVALIDARG);
            return 2;
        }
        return !strcmp(verb, "hydrate") ? filees_cfapi_hydrate(root, relative) : filees_cfapi_file_state(root, relative);
    }
    if (!strcmp(verb, "placeholders")) {
        relative[0] = L'\0';
        value = option(argc, argv, "--rel");
        if (value && *value && (!filees_cfapi_widen(value, relative, MAX_PATH) ||
                                wcsstr(relative, L"..") || relative[0] == L'\\' || wcschr(relative, L':'))) {
            filees_cfapi_fail("relative_invalid", E_INVALIDARG);
            return 2;
        }
        return filees_cfapi_placeholders(root, relative);
    }
    if (!strcmp(verb, "connect")) return filees_cfapi_connect(root);

    filees_cfapi_fail("verb_unknown", E_INVALIDARG);
    return 2;
}

/* Windows command lines are UTF-16. Narrow main() uses the system ANSI
 * codepage, whereas our parser and stdin contract require UTF-8. Convert
 * strictly at the entry point, as the native SVN helper does; never lose
 * characters or depend on the user's locale. */
int wmain(int argc, wchar_t **wide_argv)
{
    int i, result = 2;
    HRESULT failure = E_OUTOFMEMORY;
    char **argv = calloc((size_t)argc + 1, sizeof(*argv));
    if (!argv) {
        filees_cfapi_fail("arguments_invalid", failure);
        return result;
    }
    for (i = 0; i < argc; ++i) {
        int n = WideCharToMultiByte(CP_UTF8, WC_ERR_INVALID_CHARS,
                                   wide_argv[i], -1, NULL, 0, NULL, NULL);
        if (!n) {
            failure = HRESULT_FROM_WIN32(GetLastError());
            goto failed;
        }
        argv[i] = malloc((size_t)n);
        if (!argv[i]) goto failed;
        if (!WideCharToMultiByte(CP_UTF8, WC_ERR_INVALID_CHARS,
                                 wide_argv[i], -1, argv[i], n, NULL, NULL)) {
            failure = HRESULT_FROM_WIN32(GetLastError());
            goto failed;
        }
    }
    result = run(argc, argv);
    goto done;
failed:
    filees_cfapi_fail("arguments_invalid", failure);
done:
    for (i = 0; i < argc; ++i) free(argv[i]);
    free(argv);
    return result;
}
