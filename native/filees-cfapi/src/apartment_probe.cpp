// Opt-in, unshipped regression harness. Exercise the real Shell diagnostic
// repeatedly across COM teardown and force eligible DLLs to unload. No root
// registration, document access, registry changes or synthetic product verb.
#include "filees_cfapi.h"
#include <objbase.h>
#include <cstdio>
#include <cstring>

int wmain(int argc, wchar_t **argv) {
    // Synthetic invalid provider metadata, in a disposable test directory.
    // These modes never exist in the shipped helper. The cleanup verifies
    // the sentinel before unregistering; it cannot remove a normal provider.
    if (argc == 3 && (!wcscmp(argv[1], L"--corrupt-provider") || !wcscmp(argv[1], L"--remove-corrupt-provider"))) {
        const WCHAR *sentinel = L"\x0006\xc000";
        if (!wcscmp(argv[1], L"--remove-corrupt-provider")) {
            CF_SYNC_ROOT_PROVIDER_INFO current{};
            DWORD returned = 0;
            HRESULT hr = CfGetSyncRootInfoByPath(argv[2], CF_SYNC_ROOT_INFO_PROVIDER,
                &current, sizeof current, &returned);
            if (FAILED(hr) || returned != sizeof current || wcscmp(current.ProviderName, sentinel)) return 4;
            return FAILED(CfUnregisterSyncRoot(argv[2])) ? 5 : 0;
        }
        CF_SYNC_REGISTRATION registration{};
        registration.StructSize = sizeof registration;
        registration.ProviderName = sentinel;
        registration.ProviderVersion = L"1";
        CF_SYNC_POLICIES policies{};
        policies.StructSize = sizeof policies;
        policies.Hydration.Primary = CF_HYDRATION_POLICY_PROGRESSIVE;
        policies.Population.Primary = CF_POPULATION_POLICY_ALWAYS_FULL;
        return FAILED(CfRegisterSyncRoot(argv[2], &registration, &policies, CF_REGISTER_FLAG_NONE)) ? 6 : 0;
    }
    if (argc != 2) return 2;
    for (unsigned i = 0; i < 32; ++i) {
        const char *result = filees_shell_diagnostic(argv[1]);
        if (!result || strncmp(result, "{\"hr\":", 6)) return 3;
        CoFreeUnusedLibrariesEx(0, 0);
        std::printf("round=%u crypto_loaded=%d\n", i,
            GetModuleHandleW(L"CryptoWinRT.dll") != nullptr);
        std::fflush(stdout);
    }
    return 0;
}
