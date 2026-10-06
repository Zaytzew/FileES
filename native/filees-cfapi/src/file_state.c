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
    LONGLONG size;
    char identity[FILEES_CFAPI_MAX_IDENTITY * 4];
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
    out->size = standard.EndOfFile.QuadPart;
    hr = CfGetPlaceholderInfo(file, CF_PLACEHOLDER_INFO_STANDARD, info, sizeof data, &returned);
    if (SUCCEEDED(hr)) {
        out->placeholder = 1;
        out->partial = info->OnDiskDataSize.QuadPart < standard.EndOfFile.QuadPart;
        out->clean = info->InSyncState == CF_IN_SYNC_STATE_IN_SYNC;
        if (info->FileIdentityLength > 4096 ||
            returned < FIELD_OFFSET(CF_PLACEHOLDER_STANDARD_INFO, FileIdentity) + info->FileIdentityLength)
            return E_FAIL;
        out->local = filees_local_identity(info->FileIdentity, info->FileIdentityLength);
        /* Release recovery must verify the fetch identity, not just a cloud
         * icon. Copy only bounded, terminated UTF-16, without reading bytes. */
        if (info->FileIdentityLength >= sizeof(WCHAR) &&
            info->FileIdentityLength <= FILEES_CFAPI_MAX_IDENTITY * sizeof(WCHAR) &&
            info->FileIdentityLength % sizeof(WCHAR) == 0) {
            const WCHAR *identity = (const WCHAR *)info->FileIdentity;
            int count = (int)(info->FileIdentityLength / sizeof(WCHAR));
            if (identity[count - 1] == 0 && wcsnlen(identity, count) == (size_t)count - 1 &&
                !WideCharToMultiByte(CP_UTF8, WC_ERR_INVALID_CHARS, identity, count,
                                     out->identity, sizeof out->identity, NULL, NULL))
                return E_FAIL;
        }
    } else if (hr != HRESULT_FROM_WIN32(ERROR_NOT_A_CLOUD_FILE)) {
        return hr;
    } else if (out->attributes.FileAttributes & FILE_ATTRIBUTE_REPARSE_POINT) {
        return E_ACCESSDENIED; /* Never convert another kind of reparse point. */
    }
    out->partial |= (out->attributes.FileAttributes &
                    (FILE_ATTRIBUTE_RECALL_ON_DATA_ACCESS | FILE_ATTRIBUTE_RECALL_ON_OPEN)) != 0;
    return file_usn(file, &out->usn);
}

static HRESULT read_state(const WCHAR *root, const WCHAR *relative, struct state *state)
{
    WCHAR path[FILEES_CFAPI_MAX_PATH];
    HANDLE file = INVALID_HANDLE_VALUE;
    HRESULT hr = local_path(root, relative, path);
    if (SUCCEEDED(hr)) {
        file = CreateFileW(path, FILE_READ_ATTRIBUTES, FILE_SHARE_READ | FILE_SHARE_WRITE | FILE_SHARE_DELETE,
                           NULL, OPEN_EXISTING, FILE_FLAG_OPEN_REPARSE_POINT, NULL);
        hr = file == INVALID_HANDLE_VALUE ? HRESULT_FROM_WIN32(GetLastError()) : inspect(file, root, state);
    }
    if (file != INVALID_HANDLE_VALUE) CloseHandle(file);
    return hr;
}

static void print_state(const struct state *state)
{
    printf("{\"ok\":true,\"placeholder\":%s,\"partial\":%s,\"in_sync\":%s,\"local\":%s,\"usn\":\"%lld\"}",
           state->placeholder ? "true" : "false", state->partial ? "true" : "false",
           state->clean ? "true" : "false", state->local ? "true" : "false", state->usn);
}

/* Only the connected provider may prepare an adopted file for SVN exclusion.
 * This removes CFAPI metadata, NEVER bytes. A Shell clean flag is only an
 * admission check, not publication proof: the future transaction must also
 * preserve a verified copy and perform SVN pristine/status checks.
 * A share-none READ handle rejects existing readers/writers and rename while
 * this operation runs, including read-only svn:needs-lock files via WRITE_DAC.
 * No handle or reservation is handed to the subsequent SVN process. */
static HRESULT prepare_release(const WCHAR *root, const WCHAR *relative, LONGLONG expected_usn,
                               const char *expected_id)
{
    WCHAR path[FILEES_CFAPI_MAX_PATH];
    struct state before;
    BY_HANDLE_FILE_INFORMATION identity, after;
    FILE_ATTRIBUTE_TAG_INFO attributes;
    HANDLE file;
    HRESULT hr = local_path(root, relative, path);
    if (FAILED(hr) || expected_usn <= 0) return FAILED(hr) ? hr : E_INVALIDARG;
    /* Metadata-only admission avoids asking for an off-disk file's bytes. */
    hr = read_state(root, relative, &before);
    if (FAILED(hr)) return hr;
    if (!before.placeholder || !before.local || before.partial || !before.clean ||
        before.usn != expected_usn || (before.attributes.FileAttributes & FILE_ATTRIBUTE_OFFLINE))
        return HRESULT_FROM_WIN32(ERROR_RETRY);
    file = CreateFileW(path, GENERIC_READ | WRITE_DAC, 0, NULL, OPEN_EXISTING,
                       FILE_FLAG_OPEN_REPARSE_POINT, NULL);
    if (file == INVALID_HANDLE_VALUE) return HRESULT_FROM_WIN32(GetLastError());
    hr = inspect(file, root, &before);
    if (SUCCEEDED(hr) && (!before.placeholder || !before.local || before.partial || !before.clean ||
                         before.usn != expected_usn || (before.attributes.FileAttributes & FILE_ATTRIBUTE_OFFLINE)))
        hr = HRESULT_FROM_WIN32(ERROR_RETRY);
    if (SUCCEEDED(hr)) {
        if (!GetFileInformationByHandle(file, &identity)) hr = HRESULT_FROM_WIN32(GetLastError());
        else if (identity.nNumberOfLinks != 1) hr = E_ACCESSDENIED;
        else if (expected_id) {
            char actual[64];
            sprintf_s(actual, sizeof actual, "%lu:%llu", (unsigned long)identity.dwVolumeSerialNumber,
                      ((unsigned long long)identity.nFileIndexHigh << 32) | identity.nFileIndexLow);
            if (strcmp(actual, expected_id)) hr = HRESULT_FROM_WIN32(ERROR_RETRY);
        }
    }
    if (SUCCEEDED(hr)) hr = CfRevertPlaceholder(file, CF_REVERT_FLAG_NONE, NULL);
    if (SUCCEEDED(hr)) {
        if (!GetFileInformationByHandle(file, &after) ||
            !GetFileInformationByHandleEx(file, FileAttributeTagInfo, &attributes, sizeof attributes))
            hr = HRESULT_FROM_WIN32(GetLastError());
        else if ((attributes.FileAttributes & FILE_ATTRIBUTE_REPARSE_POINT) ||
                 identity.dwVolumeSerialNumber != after.dwVolumeSerialNumber ||
                 identity.nFileIndexHigh != after.nFileIndexHigh || identity.nFileIndexLow != after.nFileIndexLow ||
                 identity.nFileSizeHigh != after.nFileSizeHigh || identity.nFileSizeLow != after.nFileSizeLow ||
                 ((identity.dwFileAttributes ^ after.dwFileAttributes) & FILE_ATTRIBUTE_READONLY))
            hr = E_FAIL;
    }
    CloseHandle(file);
    return hr;
}

HRESULT filees_prepare_release(const WCHAR *root, const WCHAR *relative, LONGLONG expected_usn)
{
    return prepare_release(root, relative, expected_usn, NULL);
}

HRESULT filees_prepare_release_bound(const WCHAR *root, const WCHAR *relative,
                                    LONGLONG expected_usn, const char *expected_id)
{
    if (!expected_id || !*expected_id || strlen(expected_id) > 32) return E_INVALIDARG;
    return prepare_release(root, relative, expected_usn, expected_id);
}

/* Restore just one absent entry from the connected provider. Never convert
 * or replace an obstruction; the transaction supplies trusted repository
 * size/path metadata and retains its backup until a separate verified receipt.
 * Parent identity is checked by handle, including intermediate junctions. */
HRESULT filees_restore_release(const WCHAR *root, const WCHAR *relative, LONGLONG size)
{
    WCHAR path[FILEES_CFAPI_MAX_PATH], identity[FILEES_CFAPI_MAX_IDENTITY];
    WCHAR *name, *p;
    HANDLE parent;
    BY_HANDLE_FILE_INFORMATION parent_info;
    CF_PLACEHOLDER_CREATE_INFO entry;
    DWORD processed = 0;
    HRESULT hr = local_path(root, relative, path);
    if (FAILED(hr) || size < 0 || wcslen(relative) + 1 > FILEES_CFAPI_MAX_IDENTITY)
        return FAILED(hr) ? hr : E_INVALIDARG;
    wcscpy_s(identity, FILEES_CFAPI_MAX_IDENTITY, relative);
    for (p = identity; *p; ++p) if (*p == L'\\') *p = L'/';
    for (p = path; *p; ++p) if (*p == L'/') *p = L'\\';
    name = wcsrchr(path, L'\\');
    if (!name) return E_INVALIDARG;
    *name++ = 0;
    /* Keep the parent handle open; this is not a durable path reservation
     * across commands. Arbitrary ancestor replacement belongs to coordinator. */
    parent = CreateFileW(path, FILE_READ_ATTRIBUTES, FILE_SHARE_READ | FILE_SHARE_WRITE,
                         NULL, OPEN_EXISTING, FILE_FLAG_BACKUP_SEMANTICS, NULL);
    if (parent == INVALID_HANDLE_VALUE) return HRESULT_FROM_WIN32(GetLastError());
    hr = belongs_to_point(parent, root);
    if (SUCCEEDED(hr)) {
        if (!GetFileInformationByHandle(parent, &parent_info)) hr = HRESULT_FROM_WIN32(GetLastError());
        else if (!(parent_info.dwFileAttributes & FILE_ATTRIBUTE_DIRECTORY)) hr = E_INVALIDARG;
    }
    if (SUCCEEDED(hr)) {
        ZeroMemory(&entry, sizeof entry);
        entry.RelativeFileName = name;
        entry.FileIdentity = identity;
        entry.FileIdentityLength = (DWORD)((wcslen(identity) + 1) * sizeof(WCHAR));
        entry.FsMetadata.FileSize.QuadPart = size;
        entry.FsMetadata.BasicInfo.FileAttributes = FILE_ATTRIBUTE_NORMAL;
        entry.Flags = CF_PLACEHOLDER_CREATE_FLAG_MARK_IN_SYNC;
        hr = CfCreatePlaceholders(path, &entry, 1, CF_CREATE_FLAG_STOP_ON_ERROR, &processed);
        if (SUCCEEDED(hr) && (processed != 1 || FAILED(entry.Result)))
            hr = FAILED(entry.Result) ? entry.Result : E_FAIL;
    }
    CloseHandle(parent);
    return hr;
}

/* Explicit single-file download. WRITE_DAC on an attribute handle satisfies
 * CfHydratePlaceholder without opening for READ_DATA (which could hydrate
 * before the provider/path checks). The connected provider supplies all bytes.
 * No pin, conversion, sync-state assertion or SVN mutation here. */
int filees_cfapi_hydrate(const WCHAR *root, const WCHAR *relative)
{
    WCHAR path[FILEES_CFAPI_MAX_PATH];
    struct state state;
    HANDLE file;
    HRESULT hr = local_path(root, relative, path);
    LARGE_INTEGER offset, length;
    if (FAILED(hr)) { filees_cfapi_fail("hydrate_path", hr); return 1; }
    file = CreateFileW(path, FILE_READ_ATTRIBUTES | WRITE_DAC,
                      FILE_SHARE_READ | FILE_SHARE_WRITE | FILE_SHARE_DELETE,
                      NULL, OPEN_EXISTING, FILE_FLAG_OPEN_REPARSE_POINT, NULL);
    if (file == INVALID_HANDLE_VALUE) {
        filees_cfapi_fail("hydrate_open", HRESULT_FROM_WIN32(GetLastError()));
        return 1;
    }
    hr = inspect(file, root, &state);
    if (SUCCEEDED(hr) && state.partial) {
        offset.QuadPart = 0;
        length.QuadPart = CF_EOF;
        hr = CfHydratePlaceholder(file, offset, length, CF_HYDRATE_FLAG_NONE, NULL);
        if (SUCCEEDED(hr)) {
            hr = inspect(file, root, &state);
            if (SUCCEEDED(hr) && state.partial) hr = HRESULT_FROM_WIN32(ERROR_CLOUD_FILE_UNSUCCESSFUL);
        }
    }
    CloseHandle(file);
    if (FAILED(hr)) { filees_cfapi_fail("hydrate", hr); return 1; }
    filees_cfapi_ok("hydrated");
    return 0;
}

int filees_cfapi_file_state(const WCHAR *root, const WCHAR *relative)
{
    struct state state;
    HRESULT hr = read_state(root, relative, &state);
    if (FAILED(hr)) {
        filees_cfapi_fail("file_state", hr);
        return 1;
    }
    printf("{\"schema\":\"" FILEES_CFAPI_SCHEMA "\",\"ok\":true,\"placeholder\":%s,\"partial\":%s,\"in_sync\":%s,\"local\":%s,\"usn\":\"%lld\",\"size\":%lld,\"identity\":",
           state.placeholder ? "true" : "false", state.partial ? "true" : "false",
           state.clean ? "true" : "false", state.local ? "true" : "false", state.usn, state.size);
    filees_cfapi_json_string(state.identity);
    puts("}");
    fflush(stdout);
    return 0;
}

/* Read at most 64 relative paths in one process. No bytes, hydration or
 * mutation; failed rows keep their position and never imply a clean file.
 * Validate the complete bounded input before opening any file. */
int filees_cfapi_file_states(const WCHAR *root)
{
    WCHAR relatives[64][FILEES_CFAPI_MAX_PATH];
    char line[FILEES_CFAPI_MAX_LINE];
    int count = 0, i;
    while (fgets(line, sizeof line, stdin)) {
        size_t length = strlen(line);
        if (count == 64 || !length || line[length - 1] != '\n') {
            filees_cfapi_fail("file_states_input", E_INVALIDARG);
            return 2;
        }
        line[--length] = 0;
        if (length && line[length - 1] == '\r') line[--length] = 0;
        if (!length || !filees_cfapi_widen(line, relatives[count], FILEES_CFAPI_MAX_PATH)) {
            filees_cfapi_fail("file_states_input", E_INVALIDARG);
            return 2;
        }
        ++count;
    }
    if (ferror(stdin)) {
        filees_cfapi_fail("file_states_input", E_FAIL);
        return 2;
    }
    printf("{\"schema\":\"" FILEES_CFAPI_SCHEMA "\",\"ok\":true,\"states\":[");
    for (i = 0; i < count; ++i) {
        struct state state;
        HRESULT hr = read_state(root, relatives[i], &state);
        if (i) putchar(',');
        if (SUCCEEDED(hr)) print_state(&state);
        else printf("{\"ok\":false,\"hresult\":\"0x%08lx\"}", (unsigned long)hr);
    }
    puts("]}");
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
