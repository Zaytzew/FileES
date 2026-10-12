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

struct registry_key {
    HKEY value = nullptr;
    ~registry_key() { if (value) RegCloseKey(value); }
};

// Orphan records can be absent from GetCurrentSyncRoots. Read-only registry
// metadata binds the deterministic ID to this user's exact former path.
std::wstring registry_string(HKEY key, const WCHAR *name) {
    DWORD bytes = 0;
    check_hresult(HRESULT_FROM_WIN32(RegGetValueW(key, nullptr, name, RRF_RT_REG_SZ, nullptr, nullptr, &bytes)));
    if (bytes < sizeof(WCHAR) || bytes > FILEES_CFAPI_MAX_PATH*sizeof(WCHAR) || bytes % sizeof(WCHAR))
        throw hresult_invalid_argument();
    std::vector<WCHAR> stored(bytes/sizeof(WCHAR));
    check_hresult(HRESULT_FROM_WIN32(RegGetValueW(key, nullptr, name, RRF_RT_REG_SZ, nullptr, stored.data(), &bytes)));
    size_t length = wcsnlen_s(stored.data(), stored.size());
    if (!length || length == stored.size()) throw hresult_access_denied();
    for (size_t i = length; i < stored.size(); ++i)
        if (stored[i]) throw hresult_access_denied();
    return std::wstring(stored.data(),length);
}

std::vector<std::wstring> orphan_namespaces(const std::wstring &id, const std::wstring &path) {
    auto sid_end = id.find(L'!', 7);
    if (sid_end == std::wstring::npos) throw hresult_invalid_argument();
    auto sid = id.substr(7, sid_end - 7); // after FileES!
    auto key_path = L"Software\\Microsoft\\Windows\\CurrentVersion\\Explorer\\SyncRootManager\\" + id;
    std::vector<std::wstring> result;
    for (HKEY hive : {HKEY_CURRENT_USER, HKEY_LOCAL_MACHINE}) {
        registry_key key;
        LSTATUS status = RegOpenKeyExW(hive, key_path.c_str(), 0, KEY_READ | KEY_WOW64_64KEY, &key.value);
        if (status == ERROR_FILE_NOT_FOUND || status == ERROR_PATH_NOT_FOUND) continue;
        check_hresult(HRESULT_FROM_WIN32(status));
        registry_key roots;
        check_hresult(HRESULT_FROM_WIN32(RegOpenKeyExW(key.value,L"UserSyncRoots",0,KEY_READ,&roots.value)));
        auto stored = registry_string(roots.value,sid.c_str());
        if (canonical_path(stored.c_str()) != path) throw hresult_access_denied();
        auto clsid = registry_string(key.value,L"NamespaceCLSID");
        GUID guid{};
        if (clsid.size()!=38 || clsid.front()!=L'{' || clsid.back()!=L'}') throw hresult_invalid_argument();
        check_hresult(CLSIDFromString(clsid.c_str(),&guid));
        result.push_back(clsid);
    }
    return result;
}

void require_missing_online_root(const std::wstring &path) {
    // Do not interpret a disconnected drive/share as an obsolete point.
    if (path.size() <= 3 || path[1] != L':' || path[2] != L'\\') throw hresult_invalid_argument();
    auto drive = path.substr(0,3);
    if (GetDriveTypeW(drive.c_str()) != DRIVE_FIXED) throw hresult_access_denied();
    check_bool(GetVolumeInformationW(drive.c_str(), nullptr, 0, nullptr, nullptr, nullptr, nullptr, 0));
    if (GetFileAttributesW(path.c_str()) != INVALID_FILE_ATTRIBUTES) throw hresult_access_denied();
    DWORD error = GetLastError();
    if (error != ERROR_FILE_NOT_FOUND && error != ERROR_PATH_NOT_FOUND)
        check_hresult(HRESULT_FROM_WIN32(error));
}

}

int filees_cfapi_shell_hide_obsolete(const WCHAR *root, const WCHAR *registration_id) {
    const char *stage = "obsolete_shell_id";
    try {
        auto path = canonical_path(root);
        auto id = root_id(root);
        if (id != registration_id) throw hresult_access_denied();
        stage = "obsolete_shell_missing";
        require_missing_online_root(path);
        stage = "obsolete_shell_binding";
        for (auto const &clsid : orphan_namespaces(id,path)) {
            auto class_path = L"Software\\Classes\\CLSID\\"+clsid;
            registry_key shell, properties;
            check_hresult(HRESULT_FROM_WIN32(RegOpenKeyExW(HKEY_CURRENT_USER,class_path.c_str(),0,
                KEY_QUERY_VALUE|KEY_SET_VALUE|KEY_ENUMERATE_SUB_KEYS,&shell.value)));
            check_hresult(HRESULT_FROM_WIN32(RegOpenKeyExW(shell.value,L"Instance\\InitPropertyBag",0,KEY_READ,&properties.value)));
            auto target = registry_string(properties.value,L"TargetFolderPath");
            auto label = registry_string(shell.value,nullptr);
            if (canonical_path(target.c_str()) != path || label.rfind(L"FileES — ",0)!=0)
                throw hresult_access_denied();
            require_missing_online_root(path);
            stage = "obsolete_shell_visibility";
            DWORD hidden = 0;
            check_hresult(HRESULT_FROM_WIN32(RegSetValueExW(shell.value,L"System.IsPinnedToNameSpaceTree",0,
                REG_DWORD,reinterpret_cast<const BYTE*>(&hidden),sizeof hidden)));
            SHChangeNotify(SHCNE_ASSOCCHANGED, SHCNF_IDLIST, nullptr, nullptr);
        }
        // Do not call WinRT Unregister or CfUnregisterSyncRoot: either may
        // disconnect a still-live moved folder tracked by its FileID.
        filees_cfapi_ok("obsolete_shell_hidden");
        return 0;
    } catch (...) {
        filees_cfapi_fail(stage, to_hresult());
        return 1;
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
