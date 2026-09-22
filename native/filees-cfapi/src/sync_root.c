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

    hr = CfRegisterSyncRoot(root, &registration, &policies, CF_REGISTER_FLAG_UPDATE);
    if (FAILED(hr)) {
        filees_cfapi_fail("register_sync_root", hr);
        return 1;
    }
    filees_cfapi_ok("registered");
    return 0;
}

/* info answers one question the daemon must never assume: is this folder
 * already an anchor? Registration outlives a reboot and the daemon's own
 * lifetime, so starting one blind would either duplicate it or quietly adopt
 * somebody else's. */
int filees_cfapi_info(const WCHAR *root)
{
    CF_SYNC_ROOT_PROVIDER_INFO info;
    DWORD returned = 0;
    HRESULT hr;
    char provider[CF_MAX_PROVIDER_NAME_LENGTH + 1];

    ZeroMemory(&info, sizeof info);
    hr = CfGetSyncRootInfoByPath(root, CF_SYNC_ROOT_INFO_PROVIDER, &info, sizeof info, &returned);
    if (FAILED(hr)) {
        filees_cfapi_fail("sync_root_info", hr);
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
    HRESULT hr = CfUnregisterSyncRoot(root);
    if (FAILED(hr)) {
        filees_cfapi_fail("unregister_sync_root", hr);
        return 1;
    }
    filees_cfapi_ok("unregistered");
    return 0;
}
