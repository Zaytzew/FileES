/* Registering and unregistering one anchor folder.
 *
 * An anchor is an ordinary NTFS folder the person picked, the way they pick a
 * folder for Connect. Registration outlives a reboot; the connection in
 * connect.c does not, which is why the two are separate verbs.
 */
#include "filees_cfapi.h"

#include <cfapi.h>
#include <stdio.h>
#include <string.h>
#include <wchar.h>

/* One product GUID, stable forever: the shell keys its record of this
 * provider on it, so a new one would orphan every anchor already registered.
 * {6F1E4C3A-9B2D-4E58-A3C7-0D51F2B48E90} */
static const GUID k_provider_id = {
    0x6f1e4c3a, 0x9b2d, 0x4e58, {0xa3, 0xc7, 0x0d, 0x51, 0xf2, 0xb4, 0x8e, 0x90}
};

int filees_cfapi_register(const WCHAR *root, const WCHAR *identity)
{
    CF_SYNC_REGISTRATION registration;
    CF_SYNC_POLICIES policies;
    HRESULT hr;

    memset(&registration, 0, sizeof registration);
    registration.StructSize = sizeof registration;
    registration.ProviderName = FILEES_CFAPI_PROVIDER;
    registration.ProviderVersion = L"1";
    registration.ProviderId = k_provider_id;
    /* The identity blob comes back on every callback, so this process never
     * has to guess which repository an anchor speaks for. */
    registration.SyncRootIdentity = identity;
    registration.SyncRootIdentityLength = (DWORD)((wcslen(identity) + 1) * sizeof(WCHAR));

    memset(&policies, 0, sizeof policies);
    policies.StructSize = sizeof policies;
    /* Not ALWAYS_FULL: a file hydrated ahead of the click would never reach
     * FETCH_DATA, and fetching is the whole point - one path becomes a working
     * copy when someone opens it, not before. */
    policies.Hydration.Primary = CF_HYDRATION_POLICY_PROGRESSIVE;
    policies.Hydration.Modifier = CF_HYDRATION_POLICY_MODIFIER_NONE;
    /* The listing is seeded by the daemon (placeholders.c). Asking the filter
     * to populate on demand is the trap Nextcloud fell into, and it is out of
     * scope for this first cut. */
    policies.Population.Primary = CF_POPULATION_POLICY_ALWAYS_FULL;
    policies.Population.Modifier = CF_POPULATION_POLICY_MODIFIER_NONE;
    policies.InSync = CF_INSYNC_POLICY_TRACK_ALL;
    policies.HardLink = CF_HARDLINK_POLICY_NONE;
    policies.PlaceholderManagement = CF_PLACEHOLDER_MANAGEMENT_POLICY_DEFAULT;

    filees_registration_trace(root, "native_register_update", "before", S_OK, identity);
    hr = CfRegisterSyncRoot(root, &registration, &policies, CF_REGISTER_FLAG_UPDATE);
    filees_registration_trace(root, "native_register_update", "after", hr, identity);
    if (FAILED(hr)) {
        filees_cfapi_fail("register_sync_root", hr);
        return 1;
    }
    filees_cfapi_ok("registered");
    return 0;
}

/* Some Windows installations return another point on the volume for an
 * ordinary parent/sibling. A successful provider query is not containment
 * proof. Resolve the reported root's file ID and compare actual handle paths,
 * including a component boundary. Attribute-only handles never hydrate. */
static HRESULT verified_provider(const WCHAR *path, CF_SYNC_ROOT_PROVIDER_INFO *provider)
{
    HANDLE file = INVALID_HANDLE_VALUE, point = INVALID_HANDLE_VALUE;
    CF_SYNC_ROOT_BASIC_INFO basic;
    BY_HANDLE_FILE_INFORMATION actual;
    FILE_ID_DESCRIPTOR descriptor;
    WCHAR actual_path[FILEES_CFAPI_MAX_PATH], point_path[FILEES_CFAPI_MAX_PATH];
    DWORD returned = 0, actual_length, point_length;
    ULONGLONG file_id;
    HRESULT hr;

    file = filees_cfapi_open_path(path, FILE_READ_ATTRIBUTES, FILE_SHARE_READ | FILE_SHARE_WRITE | FILE_SHARE_DELETE,
                       NULL, OPEN_EXISTING, FILE_FLAG_BACKUP_SEMANTICS, NULL);
    if (file == INVALID_HANDLE_VALUE) return HRESULT_FROM_WIN32(GetLastError());
    ZeroMemory(&basic, sizeof basic);
    hr = CfGetSyncRootInfoByHandle(file, CF_SYNC_ROOT_INFO_BASIC, &basic, sizeof basic, &returned);
    if (FAILED(hr)) goto done;
    if (returned < sizeof basic) { hr = E_UNEXPECTED; goto done; }
    if (!GetFileInformationByHandle(file, &actual)) { hr = HRESULT_FROM_WIN32(GetLastError()); goto done; }
    file_id = ((ULONGLONG)actual.nFileIndexHigh << 32) | actual.nFileIndexLow;
    if (file_id == (ULONGLONG)basic.SyncRootFileId.QuadPart) {
        hr = CfGetSyncRootInfoByHandle(file, CF_SYNC_ROOT_INFO_PROVIDER, provider, sizeof *provider, &returned);
        goto done;
    }
    ZeroMemory(&descriptor, sizeof descriptor);
    descriptor.dwSize = sizeof descriptor;
    descriptor.Type = FileIdType;
    descriptor.FileId = basic.SyncRootFileId;
    point = OpenFileById(file, &descriptor, FILE_READ_ATTRIBUTES,
                         FILE_SHARE_READ | FILE_SHARE_WRITE | FILE_SHARE_DELETE, NULL, FILE_FLAG_BACKUP_SEMANTICS);
    if (point == INVALID_HANDLE_VALUE) { hr = HRESULT_FROM_WIN32(GetLastError()); goto done; }
    actual_length = GetFinalPathNameByHandleW(file, actual_path, FILEES_CFAPI_MAX_PATH, VOLUME_NAME_GUID);
    point_length = GetFinalPathNameByHandleW(point, point_path, FILEES_CFAPI_MAX_PATH, VOLUME_NAME_GUID);
    if (!actual_length || !point_length) { hr = HRESULT_FROM_WIN32(GetLastError()); goto done; }
    if (actual_length >= FILEES_CFAPI_MAX_PATH || point_length >= FILEES_CFAPI_MAX_PATH) {
        hr = HRESULT_FROM_WIN32(ERROR_FILENAME_EXCED_RANGE); goto done;
    }
    while (point_length && point_path[point_length - 1] == L'\\') point_path[--point_length] = L'\0';
    if (actual_length < point_length ||
        CompareStringOrdinal(actual_path, (int)point_length, point_path, (int)point_length, TRUE) != CSTR_EQUAL ||
        (actual_length > point_length && actual_path[point_length] != L'\\')) {
        hr = HRESULT_FROM_WIN32(ERROR_CLOUD_FILE_NOT_UNDER_SYNC_ROOT); goto done;
    }
    hr = CfGetSyncRootInfoByHandle(point, CF_SYNC_ROOT_INFO_PROVIDER, provider, sizeof *provider, &returned);
done:
    if (SUCCEEDED(hr) && (returned < sizeof *provider ||
        !wmemchr(provider->ProviderName, L'\0', ARRAYSIZE(provider->ProviderName)))) hr = E_UNEXPECTED;
    if (point != INVALID_HANDLE_VALUE) CloseHandle(point);
    CloseHandle(file);
    return hr;
}

/* info answers one question the daemon must never assume: is this folder
 * already an anchor? Registration outlives a reboot and the daemon's own
 * lifetime, so starting one blind would either duplicate it or quietly adopt
 * somebody else's. */
int filees_cfapi_info(const WCHAR *root)
{
    CF_SYNC_ROOT_PROVIDER_INFO info;
    HRESULT hr;
    char provider[CF_MAX_PROVIDER_NAME_LENGTH + 1];

    ZeroMemory(&info, sizeof info);
    hr = verified_provider(root, &info);
    if (filees_registration_trace_enabled()) {
        char mode[16] = {0};
        GetEnvironmentVariableA("FILEES_REGISTRATION_TRACE", mode, sizeof mode);
        if (!strcmp(mode,"sample") || FAILED(hr) || wcscmp(info.ProviderName,FILEES_CFAPI_PROVIDER))
            filees_registration_trace(root,"info","observation",hr,NULL);
    }
    if (FAILED(hr)) {
        /* Absence is not the same as inability to verify a boundary. */
        const char *code = (hr == HRESULT_FROM_WIN32(ERROR_CLOUD_FILE_NOT_UNDER_SYNC_ROOT) ||
                            hr == HRESULT_FROM_WIN32(ERROR_NOT_A_CLOUD_SYNC_ROOT) ||
                            hr == HRESULT_FROM_WIN32(ERROR_INVALID_FUNCTION)) ? "sync_root_info" : "sync_root_boundary";
        filees_cfapi_fail(code, hr);
        return 1;
    }
    if (!WideCharToMultiByte(CP_UTF8, 0, info.ProviderName, -1, provider, (int)sizeof provider, NULL, NULL)) {
        filees_cfapi_fail("provider_name_unreadable", HRESULT_FROM_WIN32(GetLastError()));
        return 1;
    }
    printf("{\"schema\":\"" FILEES_CFAPI_SCHEMA "\",\"ok\":true,\"provider\":");
    filees_cfapi_json_string(provider);
    printf(",\"status\":%lu,\"ours\":%s}\n", (unsigned long)info.ProviderStatus,
           wcscmp(info.ProviderName, FILEES_CFAPI_PROVIDER) ? "false" : "true");
    fflush(stdout);
    return 0;
}

int filees_cfapi_unregister(const WCHAR *root)
{
    HRESULT hr = filees_own_sync_root(root);
    if (FAILED(hr) && hr != HRESULT_FROM_WIN32(ERROR_CLOUD_FILE_NOT_UNDER_SYNC_ROOT) &&
        hr != HRESULT_FROM_WIN32(ERROR_FILE_NOT_FOUND) && hr != HRESULT_FROM_WIN32(ERROR_PATH_NOT_FOUND)) {
        filees_cfapi_fail("unregister_root_not_owned", hr);
        return 1;
    }
    filees_registration_trace(root, "native_unregister", "before", S_OK, NULL);
    hr = CfUnregisterSyncRoot(root);
    filees_registration_trace(root, "native_unregister", "after", hr, NULL);
    if (FAILED(hr) && hr != HRESULT_FROM_WIN32(ERROR_CLOUD_FILE_NOT_UNDER_SYNC_ROOT) &&
        hr != HRESULT_FROM_WIN32(ERROR_FILE_NOT_FOUND) && hr != HRESULT_FROM_WIN32(ERROR_PATH_NOT_FOUND)) {
        filees_cfapi_fail("unregister_sync_root", hr);
        return 1;
    }
    /* Retry also removes an orphan Shell entry after native unregister had
     * succeeded but the process stopped before Shell cleanup. */
    hr = filees_shell_unregister(root);
    if (FAILED(hr)) {
        filees_cfapi_fail("unregister_shell_root", hr);
        return 1;
    }
    filees_cfapi_ok("unregistered");
    return 0;
}

/* revert turns one materialized placeholder into an ordinary file.
 *
 * After the daemon has made a path part of the working copy (Subversion
 * adopted the hydrated file as its own), the file must stop being governed by
 * the filter: otherwise deleting or renaming it in Explorer would still meet
 * the refusal meant for paths that are not on this disk yet, and a change the
 * person makes to their own working copy would be blocked.
 *
 * The file is opened through the filter's own oplock handle, so an application
 * holding it open is not disturbed; if it holds it exclusively, the revert is
 * refused and the daemon tries again later. Directories are left as they are:
 * their children may still be placeholders.
 */
HRESULT filees_revert_placeholder(const WCHAR *path)
{
    HANDLE protected_handle = INVALID_HANDLE_VALUE, handle;
    HRESULT hr;

    hr = filees_cfapi_open_oplock(path, CF_OPEN_FILE_FLAG_WRITE_ACCESS, &protected_handle);
    if (FAILED(hr)) return hr;
    handle = CfGetWin32HandleFromProtectedHandle(protected_handle);
    hr = CfRevertPlaceholder(handle, CF_REVERT_FLAG_NONE, NULL);
    CfCloseHandle(protected_handle);
    return hr;
}

/* The standalone verb works only while no provider is connected to the
 * anchor: with one connected, Windows reports the file as in use
 * (ERROR_CLOUD_FILE_IN_USE). A running anchor reverts through its own
 * connection instead - the `revert` line on the bridge (connect.c). */
int filees_cfapi_revert(const WCHAR *path)
{
    HRESULT hr = filees_revert_placeholder(path);
    if (FAILED(hr)) {
        filees_cfapi_fail("revert_placeholder", hr);
        return 1;
    }
    filees_cfapi_ok("reverted");
    return 0;
}
