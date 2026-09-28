// The C helper owns Cloud Filter calls; this small adapter owns the supported
// per-user Shell registration. C++/WinRT comes from the Windows SDK, no runtime
// package or COM overlay extension is installed by FileES.
#include "filees_cfapi.h"
#include <cfapi.h>
#include <sddl.h>
#include <shlobj.h>
#include <winrt/Windows.Foundation.h>
#include <winrt/Windows.Foundation.Collections.h>
#include <winrt/Windows.Storage.h>
#include <winrt/Windows.Storage.Provider.h>
#include <winrt/Windows.Security.Cryptography.h>
#include <winrt/Windows.Security.Cryptography.Core.h>
#include <vector>
#include <string>

using namespace winrt;
using namespace Windows::Storage;
using namespace Windows::Storage::Provider;
using namespace Windows::Security::Cryptography;
using namespace Windows::Security::Cryptography::Core;

namespace {
const GUID provider_id = {0x6f1e4c3a, 0x9b2d, 0x4e58, {0xa3, 0xc7, 0x0d, 0x51, 0xf2, 0xb4, 0x8e, 0x90}};

struct apartment {
    apartment() { init_apartment(apartment_type::multi_threaded); }
    ~apartment() { uninit_apartment(); }
};

std::wstring canonical_path(const WCHAR *root) {
    std::vector<WCHAR> path(FILEES_CFAPI_MAX_PATH);
    DWORD n = GetFullPathNameW(root, static_cast<DWORD>(path.size()), path.data(), nullptr);
    if (!n || n >= path.size()) throw hresult_invalid_argument();
    std::wstring result(path.data(), n);
    while (result.size() > 3 && result.back() == L'\\') result.pop_back();
    // The identity is per local root, not the repository name or server address.
    CharLowerBuffW(result.data(), static_cast<DWORD>(result.size()));
    return result;
}

std::wstring root_id(const WCHAR *root) {
    HANDLE raw = nullptr;
    check_bool(OpenProcessToken(GetCurrentProcess(), TOKEN_QUERY, &raw));
    handle token{raw};
    DWORD size = 0;
    GetTokenInformation(token.get(), TokenUser, nullptr, 0, &size);
    std::vector<BYTE> buffer(size);
    check_bool(GetTokenInformation(token.get(), TokenUser, buffer.data(), size, &size));
    WCHAR *raw_sid = nullptr;
    check_bool(ConvertSidToStringSidW(reinterpret_cast<TOKEN_USER *>(buffer.data())->User.Sid, &raw_sid));
    std::wstring sid(raw_sid);
    LocalFree(raw_sid);
    auto input = CryptographicBuffer::ConvertStringToBinary(canonical_path(root), BinaryStringEncoding::Utf8);
    auto hash = HashAlgorithmProvider::OpenAlgorithm(HashAlgorithmNames::Sha256()).HashData(input);
    return L"FileES!" + sid + L"!" + std::wstring(CryptographicBuffer::EncodeToHexString(hash));
}

StorageProviderSyncRootInfo existing(const std::wstring &id) {
    // Enumerating registrations works for unpackaged desktop applications too.
    for (auto const &info : StorageProviderSyncRootManager::GetCurrentSyncRoots()) {
        if (info.Id() == id) return info;
    }
    return nullptr;
}
}

HRESULT filees_own_sync_root(const WCHAR *root) {
    try {
        // Never claim a root owned by another provider, even on a direct CLI call.
        CF_SYNC_ROOT_PROVIDER_INFO native{};
        DWORD returned = 0;
        check_hresult(CfGetSyncRootInfoByPath(root, CF_SYNC_ROOT_INFO_PROVIDER, &native, sizeof native, &returned));
        if (wcscmp(native.ProviderName, FILEES_CFAPI_PROVIDER)) throw hresult_access_denied();
        CF_SYNC_ROOT_BASIC_INFO basic{};
        check_hresult(CfGetSyncRootInfoByPath(root, CF_SYNC_ROOT_INFO_BASIC, &basic, sizeof basic, &returned));
        handle root_handle{CreateFileW(root, FILE_READ_ATTRIBUTES, FILE_SHARE_READ | FILE_SHARE_WRITE | FILE_SHARE_DELETE,
            nullptr, OPEN_EXISTING, FILE_FLAG_BACKUP_SEMANTICS | FILE_FLAG_OPEN_REPARSE_POINT, nullptr)};
        if (!root_handle) throw_last_error();
        BY_HANDLE_FILE_INFORMATION file_info{};
        check_bool(GetFileInformationByHandle(root_handle.get(), &file_info));
        auto file_id = (static_cast<ULONGLONG>(file_info.nFileIndexHigh)<<32) | file_info.nFileIndexLow;
        if (file_id != static_cast<ULONGLONG>(basic.SyncRootFileId.QuadPart)) throw hresult_access_denied();
        return S_OK;
    } catch (...) {
        return to_hresult();
    }
}

int filees_cfapi_shell_register(const WCHAR *root, const WCHAR *identity, const WCHAR *icon) {
    try {
        apartment runtime;
        check_hresult(filees_own_sync_root(root));
        auto folder = StorageFolder::GetFolderFromPathAsync(root).get();
        auto id = root_id(root);
        auto info = existing(id);
        if (info && canonical_path(info.Path().Path().c_str()) != canonical_path(root)) throw hresult_access_denied();
        if (!info) info = StorageProviderSyncRootInfo();
        info.Id(id);
        info.Path(folder);
        info.ProviderId(provider_id);
        info.DisplayNameResource(L"FileES — " + std::wstring(folder.Name()));
        info.IconResource(std::wstring(icon) + L",0");
        info.Version(L"1");
        info.HydrationPolicy(StorageProviderHydrationPolicy::Progressive);
        info.HydrationPolicyModifier(StorageProviderHydrationPolicyModifier::None);
        info.PopulationPolicy(StorageProviderPopulationPolicy::AlwaysFull);
        info.InSyncPolicy(static_cast<StorageProviderInSyncPolicy>(CF_INSYNC_POLICY_TRACK_ALL));
        info.HardlinkPolicy(StorageProviderHardlinkPolicy::None);
        // Shell pin/free-space verbs are not wired to SVN exclusion yet.
        info.AllowPinning(false);
        info.ShowSiblingsAsGroup(false);
        auto first = reinterpret_cast<const uint8_t *>(identity);
        auto length = static_cast<uint32_t>((wcslen(identity)+1)*sizeof(WCHAR));
        info.Context(CryptographicBuffer::CreateFromByteArray(array_view<const uint8_t>(first, first+length)));
        StorageProviderSyncRootManager::Register(info);
        SHChangeNotify(SHCNE_UPDATEDIR, SHCNF_PATHW, root, nullptr);
        filees_cfapi_ok("shell_registered");
        return 0;
    } catch (...) {
        filees_cfapi_fail("register_shell_root", to_hresult());
        return 1;
    }
}

HRESULT filees_shell_unregister(const WCHAR *root) {
    try {
        apartment runtime;
        auto id = root_id(root);
        auto info = existing(id);
        // GetCurrentSyncRoots omits entries whose native registration has
        // already gone. Unregister the deterministic owned ID even then.
        // Do not compare ProviderId here: after native unregister Windows can
        // retain a Shell record but no longer expose its former provider GUID.
        if (info && canonical_path(info.Path().Path().c_str()) != canonical_path(root))
            return E_ACCESSDENIED;
        StorageProviderSyncRootManager::Unregister(id);
        SHChangeNotify(SHCNE_UPDATEDIR, SHCNF_PATHW, root, nullptr);
        return S_OK;
    } catch (...) {
        HRESULT hr = to_hresult();
        return hr == HRESULT_FROM_WIN32(ERROR_NOT_FOUND) ? S_OK : hr;
    }
}
