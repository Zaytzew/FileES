/* Truthful Shell state, without changing bytes, SVN or the network.
 * A clean state requires the USN observed BEFORE the daemon checks SVN.
 * Local files stay ALWAYS_FULL: no automatic dehydration in this stage. */
#include "filees_cfapi.h"
#include <cfapi.h>
#include <winioctl.h>
#include <shlobj.h>
#include <stdio.h>
#include <stdlib.h>
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
    int placeholder, partial, clean, local, directory;
    LONGLONG on_disk, modified;
    USN usn;
    LONGLONG size;
    char identity[FILEES_CFAPI_MAX_IDENTITY * 4];
};

static HRESULT inspect_tree(HANDLE file, const WCHAR *root, struct state *out)
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
    out->directory = (out->attributes.FileAttributes & FILE_ATTRIBUTE_DIRECTORY) != 0;
    out->size = standard.EndOfFile.QuadPart;
    hr = CfGetPlaceholderInfo(file, CF_PLACEHOLDER_INFO_STANDARD, info, sizeof data, &returned);
    if (SUCCEEDED(hr)) {
        out->placeholder = 1;
        out->on_disk = info->OnDiskDataSize.QuadPart;
        out->modified = info->ModifiedDataSize.QuadPart;
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

static HRESULT inspect(HANDLE file, const WCHAR *root, struct state *out)
{
    HRESULT hr = inspect_tree(file, root, out);
    return SUCCEEDED(hr) && out->directory ? E_INVALIDARG : hr;
}

/* Only remote, clean, byte-free placeholders can change. No READ_DATA,
 * DEHYDRATE, convert/revert or recursive delete is used. A share-none handle
 * excludes readers, writes and replacement from the final proof to effect.
 * S_FALSE means a protected local obstruction, not successful mutation. */
HRESULT filees_remote_tree(const WCHAR *root, const WCHAR *relative, const WCHAR *previous,
                           const WCHAR *identity, LONGLONG size, int directory, int remove)
{
    WCHAR path[FILEES_CFAPI_MAX_PATH], *name;
    char old_utf8[FILEES_CFAPI_MAX_IDENTITY * 4], new_utf8[FILEES_CFAPI_MAX_IDENTITY * 4];
    struct state before;
    CF_FS_METADATA metadata;
    BY_HANDLE_FILE_INFORMATION object;
    HANDLE file;
    HRESULT hr = local_path(root, relative, path);
    if (FAILED(hr) || size < 0 || !*identity ||
        !WideCharToMultiByte(CP_UTF8, WC_ERR_INVALID_CHARS, previous, -1, old_utf8, sizeof old_utf8, NULL, NULL) ||
        !WideCharToMultiByte(CP_UTF8, WC_ERR_INVALID_CHARS, identity, -1, new_utf8, sizeof new_utf8, NULL, NULL))
        return FAILED(hr) ? hr : E_INVALIDARG;
    file = CreateFileW(path, FILE_READ_ATTRIBUTES | WRITE_DAC | (remove ? DELETE : 0), 0,
                       NULL, OPEN_EXISTING, FILE_FLAG_OPEN_REPARSE_POINT | FILE_FLAG_BACKUP_SEMANTICS, NULL);
    if (file == INVALID_HANDLE_VALUE) {
        DWORD error = GetLastError();
        if (error != ERROR_FILE_NOT_FOUND && error != ERROR_PATH_NOT_FOUND) return HRESULT_FROM_WIN32(error);
        if (remove) return S_OK; /* An acknowledged delete may be replayed. */
        /* Keep parent from being renamed, and prove the actual point. Caller
         * additionally retains every ancestor handle during this command. */
        for (name = path; *name; ++name) if (*name == L'/') *name = L'\\';
        name = wcsrchr(path, L'\\');
        if (!name) return E_INVALIDARG;
        *name++ = 0;
        file = CreateFileW(path, FILE_READ_ATTRIBUTES, FILE_SHARE_READ | FILE_SHARE_WRITE,
                           NULL, OPEN_EXISTING, FILE_FLAG_BACKUP_SEMANTICS, NULL);
        if (file == INVALID_HANDLE_VALUE) return HRESULT_FROM_WIN32(GetLastError());
        hr = belongs_to_point(file, root);
        if (SUCCEEDED(hr)) {
            CF_PLACEHOLDER_CREATE_INFO entry;
            DWORD processed = 0;
            ZeroMemory(&entry, sizeof entry);
            entry.RelativeFileName = name;
            entry.FileIdentity = identity;
            entry.FileIdentityLength = (DWORD)((wcslen(identity) + 1) * sizeof(WCHAR));
            entry.FsMetadata.FileSize.QuadPart = directory ? 0 : size;
            entry.FsMetadata.BasicInfo.FileAttributes = directory ? FILE_ATTRIBUTE_DIRECTORY : FILE_ATTRIBUTE_NORMAL;
            entry.Flags = CF_PLACEHOLDER_CREATE_FLAG_MARK_IN_SYNC |
                (directory ? CF_PLACEHOLDER_CREATE_FLAG_DISABLE_ON_DEMAND_POPULATION : 0);
            hr = CfCreatePlaceholders(path, &entry, 1, CF_CREATE_FLAG_STOP_ON_ERROR, &processed);
            if (SUCCEEDED(hr) && (processed != 1 || FAILED(entry.Result)))
                hr = FAILED(entry.Result) ? entry.Result : E_FAIL;
        }
        CloseHandle(file);
        return hr;
    }
    hr = inspect_tree(file, root, &before);
    if (SUCCEEDED(hr) && (!before.placeholder || before.local || !before.clean ||
        before.on_disk != 0 || before.modified != 0 || before.directory != directory ||
        (strcmp(before.identity, old_utf8) && strcmp(before.identity, new_utf8)))) hr = S_FALSE;
    if (hr == S_OK && !GetFileInformationByHandle(file, &object)) hr = HRESULT_FROM_WIN32(GetLastError());
    if (hr == S_OK && object.nNumberOfLinks != 1) hr = S_FALSE;
    if (hr == S_OK && !remove && !strcmp(before.identity, new_utf8) &&
        (directory || before.size == size)) {
        CloseHandle(file);
        return S_OK; /* Exact no-op: also supports a partly seeded restart. */
    }
    if (hr == S_OK && remove) {
        FILE_DISPOSITION_INFO disposition = { TRUE };
        /* Bound admission stays until closing the deletion handle. A folder
         * with any retained child is refused by the filesystem, never recursed. */
        filees_remote_delete_begin(object.dwVolumeSerialNumber,
            ((ULONGLONG)object.nFileIndexHigh << 32) | object.nFileIndexLow);
        if (!SetFileInformationByHandle(file, FileDispositionInfo, &disposition, sizeof disposition))
            hr = HRESULT_FROM_WIN32(GetLastError());
        CloseHandle(file);
        filees_remote_delete_end();
        return hr;
    }
    if (hr == S_OK && (strcmp(before.identity, new_utf8) || (!directory && before.size != size))) {
        ZeroMemory(&metadata, sizeof metadata);
        metadata.FileSize.QuadPart = directory ? 0 : size;
        metadata.BasicInfo.FileAttributes = before.attributes.FileAttributes;
        hr = CfUpdatePlaceholder(file, &metadata, identity, (DWORD)((wcslen(identity) + 1) * sizeof(WCHAR)),
            NULL, 0, CF_UPDATE_FLAG_VERIFY_IN_SYNC | CF_UPDATE_FLAG_MARK_IN_SYNC, NULL, NULL);
    }
    CloseHandle(file);
    return hr;
}

static HRESULT move_object(HANDLE file, const WCHAR *root, const char *expected_id,
                            BY_HANDLE_FILE_INFORMATION *object)
{
    char actual[64];
    HRESULT hr = belongs_to_point(file, root);
    if (FAILED(hr)) return hr;
    if (!GetFileInformationByHandle(file, object)) return HRESULT_FROM_WIN32(GetLastError());
    sprintf_s(actual, sizeof actual, "%lu:%llu", (unsigned long)object->dwVolumeSerialNumber,
        ((ULONGLONG)object->nFileIndexHigh << 32) | object->nFileIndexLow);
    if (!expected_id || strcmp(actual, expected_id) || object->nNumberOfLinks != 1) return E_ACCESSDENIED;
    return S_OK;
}

HRESULT filees_move_tree(const WCHAR *root, const WCHAR *source, const WCHAR *destination, const char *expected_id)
{
    WCHAR src[FILEES_CFAPI_MAX_PATH], dst[FILEES_CFAPI_MAX_PATH];
    BY_HANDLE_FILE_INFORMATION object;
    HANDLE file;
    FILE_RENAME_INFO *rename;
    DWORD bytes;
    HRESULT hr = local_path(root, source, src);
    if (SUCCEEDED(hr)) hr = local_path(root, destination, dst);
    if (FAILED(hr) || !_wcsicmp(src, dst)) return FAILED(hr) ? hr : E_INVALIDARG;
    /* The coordinator rechecks every descendant's FileID and pins ancestors.
     * Descendant data-read guards have drained before this rename (Windows
     * refuses moving the cloud tree while they are held). This handle never
     * requests bytes; each subsequent identity change has its own proof. */
    file = CreateFileW(src, FILE_READ_ATTRIBUTES | DELETE, FILE_SHARE_READ | FILE_SHARE_DELETE,
        NULL, OPEN_EXISTING, FILE_FLAG_OPEN_REPARSE_POINT | FILE_FLAG_BACKUP_SEMANTICS, NULL);
    if (file == INVALID_HANDLE_VALUE) return HRESULT_FROM_WIN32(GetLastError());
    hr = move_object(file, root, expected_id, &object);
    if (SUCCEEDED(hr) && !(object.dwFileAttributes & FILE_ATTRIBUTE_DIRECTORY)) hr = E_ACCESSDENIED;
    if (SUCCEEDED(hr)) {
        bytes = (DWORD)(sizeof *rename + wcslen(dst) * sizeof(WCHAR));
        rename = calloc(1, bytes);
        if (!rename) hr = E_OUTOFMEMORY;
        else {
            rename->ReplaceIfExists = FALSE;
            rename->FileNameLength = (DWORD)(wcslen(dst) * sizeof(WCHAR));
            memcpy(rename->FileName, dst, rename->FileNameLength);
            /* The reader has only this exact rename in flight. Other processes
             * retain the prior refusal; source FileID and helper PID bind ACK. */
            filees_remote_rename_begin(object.dwVolumeSerialNumber,
                ((ULONGLONG)object.nFileIndexHigh << 32) | object.nFileIndexLow);
            if (!SetFileInformationByHandle(file, FileRenameInfo, rename, bytes)) hr = HRESULT_FROM_WIN32(GetLastError());
            filees_remote_rename_end();
            free(rename);
        }
    }
    CloseHandle(file);
    return hr;
}

HRESULT filees_move_identity(const WCHAR *root, const WCHAR *relative, const char *expected_id,
                             const WCHAR *previous, const WCHAR *identity)
{
    WCHAR path[FILEES_CFAPI_MAX_PATH];
    char old_utf8[FILEES_CFAPI_MAX_IDENTITY * 4], new_utf8[FILEES_CFAPI_MAX_IDENTITY * 4];
    BY_HANDLE_FILE_INFORMATION object;
    struct state before = {0};
    HANDLE file;
    HRESULT hr = local_path(root, relative, path);
    if (FAILED(hr) || wcsncmp(previous, L"filees.remote/v1:", 17) || wcsncmp(identity, L"filees.remote/v1:", 17) ||
        !WideCharToMultiByte(CP_UTF8, WC_ERR_INVALID_CHARS, previous, -1, old_utf8, sizeof old_utf8, NULL, NULL) ||
        !WideCharToMultiByte(CP_UTF8, WC_ERR_INVALID_CHARS, identity, -1, new_utf8, sizeof new_utf8, NULL, NULL))
        return FAILED(hr) ? hr : E_INVALIDARG;
    file = CreateFileW(path, FILE_READ_ATTRIBUTES | WRITE_DAC, 0,
        NULL, OPEN_EXISTING, FILE_FLAG_OPEN_REPARSE_POINT | FILE_FLAG_BACKUP_SEMANTICS, NULL);
    if (file == INVALID_HANDLE_VALUE) return HRESULT_FROM_WIN32(GetLastError());
    hr = move_object(file, root, expected_id, &object);
    if (SUCCEEDED(hr)) hr = inspect_tree(file, root, &before);
    if (SUCCEEDED(hr) && (!before.placeholder || before.local ||
        (!before.directory && (before.on_disk != 0 || before.modified != 0 || !before.clean)) ||
        (strcmp(before.identity, old_utf8) && strcmp(before.identity, new_utf8)))) hr = E_ACCESSDENIED;
    if (hr == S_OK && strcmp(before.identity, new_utf8))
        /* Identity only: no FS metadata, pin-state or dehydration. VERIFY is
         * atomic for remote files; do not mark a directory's children clean.
         * Metadata handles alone are NOT a foreground write exclusion. */
        {
            hr = CfUpdatePlaceholder(file, NULL, identity, (DWORD)((wcslen(identity) + 1) * sizeof(WCHAR)),
                NULL, 0, before.directory ? CF_UPDATE_FLAG_NONE :
                CF_UPDATE_FLAG_VERIFY_IN_SYNC | CF_UPDATE_FLAG_MARK_IN_SYNC, NULL, NULL);
        }
    CloseHandle(file);
    return hr;
}

static HRESULT read_state_mode(const WCHAR *root, const WCHAR *relative, struct state *state, int tree)
{
    WCHAR path[FILEES_CFAPI_MAX_PATH];
    HANDLE file = INVALID_HANDLE_VALUE;
    HRESULT hr;
    if (tree && !wcscmp(relative, L".")) {
        hr = filees_own_sync_root(root);
        if (SUCCEEDED(hr)) wcscpy_s(path, FILEES_CFAPI_MAX_PATH, root);
    } else hr = local_path(root, relative, path);
    if (SUCCEEDED(hr)) {
        file = CreateFileW(path, FILE_READ_ATTRIBUTES, FILE_SHARE_READ | FILE_SHARE_WRITE | FILE_SHARE_DELETE,
                           NULL, OPEN_EXISTING, FILE_FLAG_OPEN_REPARSE_POINT | FILE_FLAG_BACKUP_SEMANTICS, NULL);
        hr = file == INVALID_HANDLE_VALUE ? HRESULT_FROM_WIN32(GetLastError()) :
            (tree ? inspect_tree(file, root, state) : inspect(file, root, state));
    }
    if (file != INVALID_HANDLE_VALUE) CloseHandle(file);
    return hr;
}

static HRESULT read_state(const WCHAR *root, const WCHAR *relative, struct state *state)
{
    return read_state_mode(root, relative, state, 0);
}

static void print_state(const struct state *state)
{
    printf("{\"ok\":true,\"directory\":%s,\"placeholder\":%s,\"partial\":%s,\"in_sync\":%s,\"local\":%s,\"usn\":\"%lld\",\"size\":%lld,\"on_disk\":%lld,\"modified\":%lld,\"identity\":",
           state->directory ? "true" : "false",
           state->placeholder ? "true" : "false", state->partial ? "true" : "false",
           state->clean ? "true" : "false", state->local ? "true" : "false", state->usn, state->size, state->on_disk, state->modified);
    filees_cfapi_json_string(state->identity);
    putchar('}');
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
HRESULT filees_restore_release(const WCHAR *root, const WCHAR *relative, LONGLONG size, const WCHAR *remote_identity)
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
    if (remote_identity && (!remote_identity[0] || wcslen(remote_identity) >= FILEES_CFAPI_MAX_IDENTITY)) return E_INVALIDARG;
    wcscpy_s(identity, FILEES_CFAPI_MAX_IDENTITY, remote_identity ? remote_identity : relative);
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

/* File turns keep 64 paths; a bounded directory proof can read 4096 children
 * plus its parent in one process, without thousands of helper launches.
 * No bytes, hydration or
 * mutation; failed rows keep their position and never imply a clean file.
 * Validate the complete bounded input before opening any file. */
static int states(const WCHAR *root, int tree)
{
    unsigned limit = tree ? FILEES_CFAPI_MAX_ENTRIES + 1 : 64;
    WCHAR (*relatives)[FILEES_CFAPI_MAX_PATH] = calloc(limit, sizeof *relatives);
    char line[FILEES_CFAPI_MAX_LINE];
    int count = 0, i;
    if (!relatives) { filees_cfapi_fail("file_states_memory", E_OUTOFMEMORY); return 2; }
    while (fgets(line, sizeof line, stdin)) {
        size_t length = strlen(line);
        if ((unsigned)count == limit || !length || line[length - 1] != '\n') {
            free(relatives);
            filees_cfapi_fail("file_states_input", E_INVALIDARG);
            return 2;
        }
        line[--length] = 0;
        if (length && line[length - 1] == '\r') line[--length] = 0;
        if (!length || !filees_cfapi_widen(line, relatives[count], FILEES_CFAPI_MAX_PATH)) {
            free(relatives);
            filees_cfapi_fail("file_states_input", E_INVALIDARG);
            return 2;
        }
        ++count;
    }
    if (ferror(stdin)) {
        free(relatives);
        filees_cfapi_fail("file_states_input", E_FAIL);
        return 2;
    }
    printf("{\"schema\":\"" FILEES_CFAPI_SCHEMA "\",\"ok\":true,\"states\":[");
    for (i = 0; i < count; ++i) {
        struct state state;
        HRESULT hr = read_state_mode(root, relatives[i], &state, tree);
        if (i) putchar(',');
        if (SUCCEEDED(hr)) print_state(&state);
        else printf("{\"ok\":false,\"hresult\":\"0x%08lx\"}", (unsigned long)hr);
    }
    puts("]}");
    free(relatives);
    fflush(stdout);
    return 0;
}

int filees_cfapi_file_states(const WCHAR *root) { return states(root, 0); }
int filees_cfapi_tree_states(const WCHAR *root) { return states(root, 1); }

/* Confirm ONE directory, never its descendants. The daemon supplies the exact
 * complete bounded subtree observed before SVN inspection. Attribute-only handles
 * keep those objects/USNs stable through the parent CAS, without hydration.
 * Pin state and population policy are not availability decorations. */
HRESULT filees_set_directory_state(const WCHAR *root, const WCHAR *relative,
    const WCHAR *identity, LONGLONG expected_usn, int clean, char **lines, unsigned input_count)
{
    struct child { WCHAR name[FILEES_CFAPI_MAX_PATH]; HANDLE file; int directory; USN usn; };
    struct child *children = NULL;
    WCHAR path[FILEES_CFAPI_MAX_PATH], full[FILEES_CFAPI_MAX_PATH], wanted[FILEES_CFAPI_MAX_IDENTITY];
    HANDLE directory = INVALID_HANDLE_VALUE, search = INVALID_HANDLE_VALUE;
    WIN32_FIND_DATAW found;
    struct state before, child_state;
    unsigned count = 0, i, seen = 0, row = 0, parent, pass;
    int confirmed = 0, bound_parent = 0;
    HRESULT hr = E_INVALIDARG;
    if (expected_usn <= 0 || input_count > FILEES_CFAPI_MAX_ENTRIES || (!clean && input_count)) goto done;
    children = calloc(input_count ? input_count : 1, sizeof *children);
    if (!children) return E_OUTOFMEMORY;
    for (i = 0; i < input_count; ++i) children[i].file = INVALID_HANDLE_VALUE;
    if (!wcscmp(relative, L".")) {
        hr = filees_own_sync_root(root);
        if (SUCCEEDED(hr)) wcscpy_s(path, FILEES_CFAPI_MAX_PATH, root);
    } else hr = local_path(root, relative, path);
    if (FAILED(hr)) goto done;
    directory = CreateFileW(path, FILE_LIST_DIRECTORY | FILE_READ_ATTRIBUTES | WRITE_DAC,
        FILE_SHARE_READ, NULL, OPEN_EXISTING, FILE_FLAG_OPEN_REPARSE_POINT | FILE_FLAG_BACKUP_SEMANTICS, NULL);
    if (directory == INVALID_HANDLE_VALUE) { hr = HRESULT_FROM_WIN32(GetLastError()); goto done; }
    hr = inspect_tree(directory, root, &before);
    if (FAILED(hr)) goto done;
    if (!before.directory || before.usn != expected_usn ||
        !filees_cfapi_widen(before.identity, wanted, FILEES_CFAPI_MAX_IDENTITY) || wcscmp(wanted, identity)) {
        hr = HRESULT_FROM_WIN32(ERROR_RETRY); goto done;
    }
    bound_parent = 1;
    for (row = 0; row < input_count; ++row) {
        char *line = lines[row];
        char *token, *id, *end = NULL;
        LONGLONG child_usn;
        size_t n = strlen(line);
        if (count >= FILEES_CFAPI_MAX_ENTRIES || !n || n + 1 >= FILEES_CFAPI_MAX_LINE) { hr = E_INVALIDARG; goto done; }
        while (n && (line[n-1] == '\r' || line[n-1] == '\n')) line[--n] = 0;
        token = strchr(line, '\t');
        if (!token) { hr = E_INVALIDARG; goto done; }
        *token++ = 0; id = strchr(token, '\t');
        if (!id) { hr = E_INVALIDARG; goto done; }
        *id++ = 0; child_usn = _strtoi64(token, &end, 10);
        if (!clean || !end || end == token || *end || child_usn <= 0 || strchr(id, '\t') ||
            !filees_cfapi_widen(line, children[count].name, FILEES_CFAPI_MAX_PATH) ||
            !*children[count].name || wcspbrk(children[count].name, L"\\:*?\"<>|") ||
            !wcscmp(children[count].name, L".") || !wcscmp(children[count].name, L"..") ||
            !_wcsicmp(children[count].name, L".svn") || !_wcsicmp(children[count].name, L".filees") ||
            !filees_cfapi_widen(id, wanted, FILEES_CFAPI_MAX_IDENTITY)) { hr = E_INVALIDARG; goto done; }
        for (i = 0; i < count; ++i)
            if (!_wcsicmp(children[i].name, children[count].name)) { hr = E_INVALIDARG; goto done; }
        if (swprintf_s(full, FILEES_CFAPI_MAX_PATH, !wcscmp(relative,L".") ? L"%s" : L"%s/%s",
            !wcscmp(relative,L".") ? children[count].name : relative, children[count].name) < 0) { hr = E_INVALIDARG; goto done; }
        { WCHAR canonical[FILEES_CFAPI_MAX_PATH];
          hr = local_path(root,full,canonical);
          if (FAILED(hr)) goto done;
          wcscpy_s(full,FILEES_CFAPI_MAX_PATH,canonical);
        }
        children[count].file = CreateFileW(full, FILE_READ_ATTRIBUTES, FILE_SHARE_READ, NULL,
            OPEN_EXISTING, FILE_FLAG_OPEN_REPARSE_POINT | FILE_FLAG_BACKUP_SEMANTICS, NULL);
        if (children[count].file == INVALID_HANDLE_VALUE) { hr = HRESULT_FROM_WIN32(GetLastError()); goto done; }
        ++count;
        hr = inspect_tree(children[count-1].file, root, &child_state);
        if (FAILED(hr)) goto done;
        if (!child_state.placeholder || !child_state.clean || child_state.usn != child_usn || child_state.modified != 0 ||
            !filees_cfapi_widen(child_state.identity, wanted, FILEES_CFAPI_MAX_IDENTITY) || strcmp(child_state.identity, id)) {
            hr = HRESULT_FROM_WIN32(ERROR_RETRY); goto done;
        }
        children[count-1].directory = child_state.directory;
        children[count-1].usn = child_state.usn;
    }
    if (clean) {
        for (pass = 0; pass < 2; ++pass) {
        seen = 0;
        for (parent = 0; parent <= count; ++parent) {
        const WCHAR *prefix = parent ? children[parent-1].name : L"";
        if (parent && !children[parent-1].directory) continue;
        if (swprintf_s(full, FILEES_CFAPI_MAX_PATH, *prefix ? L"%s\\%s\\*" : L"%s\\*", path, prefix) < 0) { hr = E_INVALIDARG; goto done; }
        search = FindFirstFileW(full, &found);
        if (search == INVALID_HANDLE_VALUE && GetLastError() != ERROR_FILE_NOT_FOUND) { hr = HRESULT_FROM_WIN32(GetLastError()); goto done; }
        if (search != INVALID_HANDLE_VALUE) do {
            if (!wcscmp(found.cFileName, L".") || !wcscmp(found.cFileName, L"..")) continue;
            if (!parent && !wcscmp(relative, L".") &&
                (!_wcsicmp(found.cFileName, L".svn") || !_wcsicmp(found.cFileName, L".filees"))) continue;
            if (swprintf_s(full,FILEES_CFAPI_MAX_PATH,*prefix ? L"%s/%s" : L"%s",
                *prefix ? prefix : found.cFileName, found.cFileName) < 0) { hr=E_INVALIDARG;goto done; }
            for (i = 0; i < count; ++i) if (!wcscmp(children[i].name, full)) break;
            if (i == count) { hr = HRESULT_FROM_WIN32(ERROR_RETRY); goto done; }
            ++seen;
        } while (FindNextFileW(search, &found));
        if (search != INVALID_HANDLE_VALUE && GetLastError() != ERROR_NO_MORE_FILES) { hr = HRESULT_FROM_WIN32(GetLastError()); goto done; }
        if (search != INVALID_HANDLE_VALUE) { FindClose(search);search=INVALID_HANDLE_VALUE; }
        }
        if (seen != count) { hr = HRESULT_FROM_WIN32(ERROR_RETRY); goto done; }
        /* Directory handles do not prevent creation of a new descendant.
         * Repeat all directory tokens after enumeration, while existing byte
         * objects remain deny-write pinned. Never ACK a changed listing. */
        for (i=0;i<count;++i) if (children[i].directory) {
            USN final_usn;
            hr=file_usn(children[i].file,&final_usn);
            if (FAILED(hr)) goto done;
            if (final_usn!=children[i].usn) {hr=HRESULT_FROM_WIN32(ERROR_RETRY);goto done;}
        }
        { USN final_usn;
          hr=file_usn(directory,&final_usn);
          if (FAILED(hr)) goto done;
          if (final_usn!=expected_usn) {hr=HRESULT_FROM_WIN32(ERROR_RETRY);goto done;}
        }
        if (!pass) {
            USN acknowledged_usn = 0;
            /* Directory handles cannot forbid creation of a new child.
             * Validate the complete listing again AFTER setting the marker;
             * a changed proof clears only this parent before returning.
             * Later creations are tracked normally by TRACK_ALL. */
            hr = CfSetInSyncState(directory, CF_IN_SYNC_STATE_IN_SYNC,
                CF_SET_IN_SYNC_FLAG_NONE, &acknowledged_usn);
            if (FAILED(hr)) goto done;
            confirmed = 1;
            /* Some cldflt versions return zero for a directory's output USN.
             * The complete post-ACK enumeration is the essential proof;
             * record its starting directory token on this same handle. */
            hr = file_usn(directory, &expected_usn);
            if (FAILED(hr)) goto done;
        }
        }
    } else {
        hr = CfSetInSyncState(directory, CF_IN_SYNC_STATE_NOT_IN_SYNC,
            CF_SET_IN_SYNC_FLAG_NONE, NULL);
    }
done:
    if (bound_parent && clean && FAILED(hr))
        CfSetInSyncState(directory, CF_IN_SYNC_STATE_NOT_IN_SYNC, CF_SET_IN_SYNC_FLAG_NONE, NULL);
    if (search != INVALID_HANDLE_VALUE) FindClose(search);
    for (i = 0; i < count; ++i) if (children[i].file != INVALID_HANDLE_VALUE) CloseHandle(children[i].file);
    if (directory != INVALID_HANDLE_VALUE) CloseHandle(directory);
    free(children);
    if (SUCCEEDED(hr) || confirmed || bound_parent) SHChangeNotify(SHCNE_UPDATEITEM, SHCNF_PATHW, path, NULL);
    return hr;
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
