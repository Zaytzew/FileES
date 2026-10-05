/* Truthful Shell state, without changing bytes, SVN or the network.
 * A clean state requires the USN observed BEFORE the daemon checks SVN.
 * Local files stay ALWAYS_FULL: no automatic dehydration in this stage. */
#include "filees_cfapi.h"
#include <cfapi.h>
#include <winioctl.h>
#include <shlobj.h>
#include <stdio.h>
#include <string.h>

static const WCHAR k_local_identity[] = L"filees.local/v1";

int filees_local_identity(const void *identity, DWORD length)
{
    return identity && length == sizeof k_local_identity &&
           !memcmp(identity, k_local_identity, sizeof k_local_identity);
}

static HRESULT local_path(const WCHAR *root, const WCHAR *rel, WCHAR *out)
{
    const WCHAR *part = rel, *p;
    size_t length;
    HRESULT hr = filees_own_sync_root(root);
    if (FAILED(hr)) return hr;
    if (!rel || !*rel || *rel == L'\\' || *rel == L'/') return E_INVALIDARG;
    for (p = rel; ; ++p) {
        if (*p == L':' || *p == L'\t' || *p == L'\n' || *p == L'\r') return E_INVALIDARG;
        if (!*p || *p == L'/' || *p == L'\\') {
            length = (size_t)(p - part);
            if (!length || part[length - 1] == L'.' || part[length - 1] == L' ' ||
                (length == 1 && *part == L'.') ||
                (length == 2 && !wcsncmp(part, L"..", 2)) ||
                (length == 4 && !_wcsnicmp(part, L".svn", 4)) ||
                (length == 7 && !_wcsnicmp(part, L".filees", 7))) return E_INVALIDARG;
            if (!*p) break;
            part = p + 1;
        }
    }
    if (swprintf_s(out, FILEES_CFAPI_MAX_PATH, L"%s\\%s", root, rel) < 0) return E_INVALIDARG;
    return S_OK;
}

static HRESULT file_usn(HANDLE file, USN *usn)
{
    BYTE data[4096];
    DWORD returned = 0;
    READ_FILE_USN_DATA input = {2, 2};
    USN_RECORD_V2 *record = (USN_RECORD_V2 *)data;
    if (!DeviceIoControl(file, FSCTL_READ_FILE_USN_DATA, &input, sizeof input,
                         data, sizeof data, &returned, NULL))
        return HRESULT_FROM_WIN32(GetLastError());
    if (returned < FIELD_OFFSET(USN_RECORD_V2, FileName) || record->MajorVersion != 2 || !record->Usn)
        return E_FAIL;
    *usn = record->Usn;
    return S_OK;
}

/* An intermediate junction can lead outside this point. Compare the opened
 * object's actual sync-root ID, not only its lexical path/provider name. */
static HRESULT belongs_to_point(HANDLE file, const WCHAR *root)
{
    CF_SYNC_ROOT_BASIC_INFO basic;
    BY_HANDLE_FILE_INFORMATION info, actual;
    DWORD returned = 0;
    HANDLE point;
    ULONGLONG id;
    HRESULT hr = CfGetSyncRootInfoByHandle(file, CF_SYNC_ROOT_INFO_BASIC, &basic, sizeof basic, &returned);
    if (FAILED(hr)) return hr;
    if (!GetFileInformationByHandle(file, &actual)) return HRESULT_FROM_WIN32(GetLastError());
    point = CreateFileW(root, FILE_READ_ATTRIBUTES, FILE_SHARE_READ | FILE_SHARE_WRITE | FILE_SHARE_DELETE,
                        NULL, OPEN_EXISTING, FILE_FLAG_BACKUP_SEMANTICS | FILE_FLAG_OPEN_REPARSE_POINT, NULL);
    if (point == INVALID_HANDLE_VALUE) return HRESULT_FROM_WIN32(GetLastError());
    if (!GetFileInformationByHandle(point, &info)) {
        hr = HRESULT_FROM_WIN32(GetLastError());
        CloseHandle(point);
        return hr;
    }
    CloseHandle(point);
    id = ((ULONGLONG)info.nFileIndexHigh << 32) | info.nFileIndexLow;
    return info.dwVolumeSerialNumber == actual.dwVolumeSerialNumber &&
           id == (ULONGLONG)basic.SyncRootFileId.QuadPart ? S_OK : E_ACCESSDENIED;
}

struct state {
    FILE_ATTRIBUTE_TAG_INFO attributes;
    int placeholder, partial, clean, local;
    USN usn;
};

static HRESULT inspect(HANDLE file, const WCHAR *root, struct state *out)
{
    BYTE data[sizeof(CF_PLACEHOLDER_STANDARD_INFO) + 4096];
    CF_PLACEHOLDER_STANDARD_INFO *info = (CF_PLACEHOLDER_STANDARD_INFO *)data;
    FILE_STANDARD_INFO standard;
    DWORD returned = 0;
    HRESULT hr = belongs_to_point(file, root);
    if (FAILED(hr)) return hr;
    ZeroMemory(out, sizeof *out);
    if (!GetFileInformationByHandleEx(file, FileAttributeTagInfo, &out->attributes, sizeof out->attributes) ||
        !GetFileInformationByHandleEx(file, FileStandardInfo, &standard, sizeof standard))
        return HRESULT_FROM_WIN32(GetLastError());
    if (out->attributes.FileAttributes & FILE_ATTRIBUTE_DIRECTORY) return E_INVALIDARG;
    hr = CfGetPlaceholderInfo(file, CF_PLACEHOLDER_INFO_STANDARD, info, sizeof data, &returned);
    if (SUCCEEDED(hr)) {
        out->placeholder = 1;
        out->partial = info->OnDiskDataSize.QuadPart < standard.EndOfFile.QuadPart;
        out->clean = info->InSyncState == CF_IN_SYNC_STATE_IN_SYNC;
        if (info->FileIdentityLength > 4096) return E_FAIL;
        out->local = filees_local_identity(info->FileIdentity, info->FileIdentityLength);
    } else if (hr != HRESULT_FROM_WIN32(ERROR_NOT_A_CLOUD_FILE)) {
        return hr;
    } else if (out->attributes.FileAttributes & FILE_ATTRIBUTE_REPARSE_POINT) {
        return E_ACCESSDENIED; /* Never convert another kind of reparse point. */
    }
    out->partial |= (out->attributes.FileAttributes &
                    (FILE_ATTRIBUTE_RECALL_ON_DATA_ACCESS | FILE_ATTRIBUTE_RECALL_ON_OPEN)) != 0;
    return file_usn(file, &out->usn);
}

int filees_cfapi_file_state(const WCHAR *root, const WCHAR *relative)
{
    WCHAR path[FILEES_CFAPI_MAX_PATH];
    struct state state;
    HANDLE file = INVALID_HANDLE_VALUE;
    HRESULT hr = local_path(root, relative, path);
    if (SUCCEEDED(hr)) {
        file = CreateFileW(path, FILE_READ_ATTRIBUTES, FILE_SHARE_READ | FILE_SHARE_WRITE | FILE_SHARE_DELETE,
                           NULL, OPEN_EXISTING, FILE_FLAG_OPEN_REPARSE_POINT, NULL);
        hr = file == INVALID_HANDLE_VALUE ? HRESULT_FROM_WIN32(GetLastError()) : inspect(file, root, &state);
    }
    if (file != INVALID_HANDLE_VALUE) CloseHandle(file);
    if (FAILED(hr)) {
        filees_cfapi_fail("file_state", hr);
        return 1;
    }
    printf("{\"schema\":\"" FILEES_CFAPI_SCHEMA "\",\"ok\":true,\"placeholder\":%s,\"partial\":%s,\"in_sync\":%s,\"local\":%s,\"usn\":\"%lld\"}\n",
           state.placeholder ? "true" : "false", state.partial ? "true" : "false",
           state.clean ? "true" : "false", state.local ? "true" : "false", state.usn);
    fflush(stdout);
    return 0;
}

HRESULT filees_set_local_state(const WCHAR *root, const WCHAR *relative, LONGLONG expected_usn, int clean)
{
    WCHAR path[FILEES_CFAPI_MAX_PATH];
    HANDLE protected_handle = INVALID_HANDLE_VALUE, file;
    int referenced = 0, raw = 0;
    struct state before;
    USN usn;
    HRESULT hr = local_path(root, relative, path);
    if (FAILED(hr) || expected_usn <= 0) return FAILED(hr) ? hr : E_INVALIDARG;
    /* CfOpenFileWithOplock requests data access. Refuse an off-disk file
     * through an attribute-only handle first, so the refusal never fetches. */
    file = CreateFileW(path, FILE_READ_ATTRIBUTES, FILE_SHARE_READ | FILE_SHARE_WRITE | FILE_SHARE_DELETE,
                       NULL, OPEN_EXISTING, FILE_FLAG_OPEN_REPARSE_POINT, NULL);
    if (file == INVALID_HANDLE_VALUE) return HRESULT_FROM_WIN32(GetLastError());
    hr = inspect(file, root, &before);
    CloseHandle(file);
    if (FAILED(hr)) return hr;
    if (before.partial || before.usn != expected_usn) return HRESULT_FROM_WIN32(ERROR_RETRY);
    hr = CfOpenFileWithOplock(path, CF_OPEN_FILE_FLAG_WRITE_ACCESS | CF_OPEN_FILE_FLAG_EXCLUSIVE, &protected_handle);
    if (hr == HRESULT_FROM_WIN32(ERROR_ACCESS_DENIED) &&
        (before.attributes.FileAttributes & FILE_ATTRIBUTE_READONLY)) {
        /* svn:needs-lock must stay read-only. CFAPI accepts WRITE_DAC for
         * metadata; a short share-none, attribute-only handle supplies it
         * without clearing READONLY or requesting the file's bytes. */
        file = CreateFileW(path, FILE_READ_ATTRIBUTES | WRITE_DAC, 0, NULL, OPEN_EXISTING,
                           FILE_FLAG_OPEN_REPARSE_POINT, NULL);
        if (file == INVALID_HANDLE_VALUE) return HRESULT_FROM_WIN32(GetLastError());
        raw = 1;
    } else {
        if (FAILED(hr)) return hr;
        if (!CfReferenceProtectedHandle(protected_handle)) {
            CfCloseHandle(protected_handle);
            return HRESULT_FROM_WIN32(ERROR_RETRY);
        }
        referenced = 1;
        file = CfGetWin32HandleFromProtectedHandle(protected_handle);
    }
    hr = inspect(file, root, &before);
    if (SUCCEEDED(hr) && (before.partial || before.usn != expected_usn))
        hr = HRESULT_FROM_WIN32(ERROR_RETRY);
    if (SUCCEEDED(hr)) {
        if (!before.placeholder)
            hr = CfConvertToPlaceholder(file, k_local_identity, sizeof k_local_identity,
                                         CF_CONVERT_FLAG_ALWAYS_FULL, &usn, NULL);
        else if (!before.local)
            hr = CfUpdatePlaceholder(file, NULL, k_local_identity, sizeof k_local_identity, NULL, 0,
                                     CF_UPDATE_FLAG_ALWAYS_FULL | CF_UPDATE_FLAG_CLEAR_IN_SYNC, NULL, NULL);
    }
    /* USN was compared while holding the exclusive, referenced oplock.
     * Conversion/update changes USN itself. No other writer can enter until
     * release, so the second call does not reuse a pre-conversion token. */
    if (SUCCEEDED(hr))
        hr = CfSetInSyncState(file, clean ? CF_IN_SYNC_STATE_IN_SYNC : CF_IN_SYNC_STATE_NOT_IN_SYNC,
                             CF_SET_IN_SYNC_FLAG_NONE, NULL);
    if (referenced) CfReleaseProtectedHandle(protected_handle);
    if (raw) CloseHandle(file);
    else CfCloseHandle(protected_handle);
    if (SUCCEEDED(hr)) SHChangeNotify(SHCNE_UPDATEITEM, SHCNF_PATHW, path, NULL);
    return hr;
}
