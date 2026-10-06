// Native test fixture only. Use a new temporary directory, never a user WC.
// It registers a synthetic independent provider without connecting callbacks.
#define _WIN32_WINNT 0x0A00
#include <windows.h>
#include <cfapi.h>
#include <stdio.h>
#include <string.h>

int wmain(int argc, wchar_t **argv) {
    if (argc != 3) return 2;
    if (!wcscmp(argv[1], L"remove"))
        return FAILED(CfUnregisterSyncRoot(argv[2]));
    if (wcscmp(argv[1], L"create")) return 2;

    CF_SYNC_REGISTRATION r = {0};
    CF_SYNC_POLICIES p = {0};
    r.StructSize = sizeof(r);
    r.ProviderName = L"Independent FileES acceptance fixture";
    r.ProviderVersion = L"1";
    wchar_t rootidentity[] = L"independent-lab";
    r.SyncRootIdentity = rootidentity;
    r.SyncRootIdentityLength = sizeof(rootidentity);
    GUID id = {0xe454c1af, 0x7c50, 0x48ab,
               {0xb1, 0x51, 0x38, 0xcb, 0x6c, 0x82, 0xb0, 0x70}};
    r.ProviderId = id;
    p.StructSize = sizeof(p);
    p.Hydration.Primary = CF_HYDRATION_POLICY_PROGRESSIVE;
    p.Population.Primary = CF_POPULATION_POLICY_ALWAYS_FULL;
    p.InSync = CF_INSYNC_POLICY_TRACK_ALL;
    HRESULT hr = CfRegisterSyncRoot(argv[2], &r, &p, CF_REGISTER_FLAG_NONE);
    if (FAILED(hr)) {
        printf("register HRESULT=%08lx\n", hr);
        return 3;
    }

    CF_PLACEHOLDER_CREATE_INFO f = {0};
    f.RelativeFileName = L"unavailable.dwg";
    f.FsMetadata.FileSize.QuadPart = 4096;
    f.FsMetadata.BasicInfo.FileAttributes = FILE_ATTRIBUTE_NORMAL;
    char identity[] = "independent-lab-object";
    f.FileIdentity = identity;
    f.FileIdentityLength = sizeof(identity);
    f.Flags = CF_PLACEHOLDER_CREATE_FLAG_MARK_IN_SYNC;
    DWORD done = 0;
    hr = CfCreatePlaceholders(argv[2], &f, 1, CF_CREATE_FLAG_NONE, &done);
    printf("create HRESULT=%08lx result=%08lx processed=%lu\n", hr, f.Result, done);
    return FAILED(hr) || FAILED(f.Result) || done != 1;
}
