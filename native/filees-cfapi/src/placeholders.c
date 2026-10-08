/* Seeding one directory of the anchor with placeholders.
 *
 * The listing arrives on stdin, one entry per line, tab separated:
 *
 *     kind <TAB> size <TAB> identity <TAB> name
 *
 * kind is "f" or "d", size is decimal bytes (0 for a directory), identity is
 * the repository path the daemon wants back on a callback, and name is the
 * entry's own name - last, because it is the only field that may contain
 * spaces. A tab or a newline in a name is refused rather than guessed at.
 *
 * Nothing here talks to a server: the daemon has already read HEAD.
 */
#include "filees_cfapi.h"

#include <cfapi.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

static int split_line(char *line, char **kind, char **size, char **identity, char **name)
{
    char *cursor = line;
    *kind = cursor;
    cursor = strchr(cursor, '\t');
    if (!cursor) return 0;
    *cursor++ = '\0';
    *size = cursor;
    cursor = strchr(cursor, '\t');
    if (!cursor) return 0;
    *cursor++ = '\0';
    *identity = cursor;
    cursor = strchr(cursor, '\t');
    if (!cursor) return 0;
    *cursor++ = '\0';
    *name = cursor;
    return **kind && **name && !strchr(*name, '\t');
}

/* A name from a listing is data, not a path expression: anything that could
 * leave the directory it is being created in is refused here rather than
 * handed to the filter. */
static int plain_name(const WCHAR *name)
{
    if (!name[0] || !wcscmp(name, L".") || !wcscmp(name, L"..")) return 0;
    return !wcspbrk(name, L"\\/:*?\"<>|");
}

static int read_entries(struct filees_entry *entries, unsigned *count, const char **refusal)
{
    char line[FILEES_CFAPI_MAX_LINE];
    *count = 0;
    while (fgets(line, sizeof line, stdin)) {
        char *kind, *size, *identity, *name;
        size_t length = strlen(line);
        struct filees_entry *entry;
        while (length && (line[length - 1] == '\n' || line[length - 1] == '\r')) line[--length] = '\0';
        if (!length) continue;
        if (length + 1 >= sizeof line) { *refusal = "listing_line_too_long"; return 0; }
        if (*count >= FILEES_CFAPI_MAX_ENTRIES) { *refusal = "listing_too_large"; return 0; }
        if (!split_line(line, &kind, &size, &identity, &name)) { *refusal = "listing_malformed"; return 0; }
        entry = &entries[*count];
        memset(entry, 0, sizeof *entry);
        entry->directory = (*kind == 'd');
        entry->size = _atoi64(size);
        if (entry->size < 0) { *refusal = "listing_malformed"; return 0; }
        if (!filees_cfapi_widen(name, entry->name, MAX_PATH)) { *refusal = "listing_name_not_utf8"; return 0; }
        if (!plain_name(entry->name)) { *refusal = "listing_name_refused"; return 0; }
        if (!filees_cfapi_widen(identity, entry->identity, FILEES_CFAPI_MAX_IDENTITY)) {
            *refusal = "listing_identity_not_utf8";
            return 0;
        }
        ++*count;
    }
    return 1;
}

/* Opens the entry itself, not what it stands for: nothing is hydrated. The
 * directory listing cannot answer this; unreadable metadata shows only when
 * the entry is opened. */
static HRESULT entry_readable(const WCHAR *directory, const WCHAR *name)
{
    WCHAR path[MAX_PATH * 3];
    HANDLE handle;
    if (_snwprintf_s(path, MAX_PATH * 3, _TRUNCATE, L"%s\\%s", directory, name) < 0) return E_INVALIDARG;
    handle = CreateFileW(path, FILE_READ_ATTRIBUTES, FILE_SHARE_READ | FILE_SHARE_WRITE | FILE_SHARE_DELETE, NULL,
                         OPEN_EXISTING, FILE_FLAG_OPEN_REPARSE_POINT | FILE_FLAG_BACKUP_SEMANTICS, NULL);
    if (handle == INVALID_HANDLE_VALUE) return HRESULT_FROM_WIN32(GetLastError());
    CloseHandle(handle);
    return S_OK;
}

int filees_cfapi_placeholders(const WCHAR *root, const WCHAR *relative)
{
    struct filees_entry *entries;
    CF_PLACEHOLDER_CREATE_INFO *infos;
    WCHAR directory[MAX_PATH * 2];
    const char *refusal = NULL;
    unsigned count = 0, i;
    DWORD processed = 0;
    HRESULT hr;
    int status = 0;

    entries = calloc(FILEES_CFAPI_MAX_ENTRIES, sizeof *entries);
    infos = calloc(FILEES_CFAPI_MAX_ENTRIES, sizeof *infos);
    if (!entries || !infos) {
        free(entries);
        free(infos);
        filees_cfapi_fail("out_of_memory", E_OUTOFMEMORY);
        return 1;
    }
    if (!read_entries(entries, &count, &refusal)) {
        free(entries);
        free(infos);
        filees_cfapi_fail(refusal ? refusal : "listing_malformed", E_INVALIDARG);
        return 1;
    }

    _snwprintf_s(directory, MAX_PATH * 2, _TRUNCATE, relative && relative[0] ? L"%s\\%s" : L"%s", root, relative);

    for (i = 0; i < count; ++i) {
        CF_PLACEHOLDER_CREATE_INFO *info = &infos[i];
        info->RelativeFileName = entries[i].name;
        info->FileIdentity = entries[i].identity;
        info->FileIdentityLength = (DWORD)((wcslen(entries[i].identity) + 1) * sizeof(WCHAR));
        info->FsMetadata.FileSize.QuadPart = entries[i].directory ? 0 : entries[i].size;
        info->FsMetadata.BasicInfo.FileAttributes =
            entries[i].directory ? FILE_ATTRIBUTE_DIRECTORY : FILE_ATTRIBUTE_NORMAL;
        /* Marked in sync: the placeholder matches HEAD as the daemon read it.
         * Nothing is on disk yet, and that is not a change to upload.
         *
         * A directory also has to say it is already complete. The filter
         * otherwise expects to ask for its contents on demand, which this
         * first cut deliberately does not answer, and refuses to create it
         * at all (ERROR_CLOUD_FILE_NOT_SUPPORTED). The daemon seeds each
         * level itself, so "complete" is the truth here. */
        info->Flags = CF_PLACEHOLDER_CREATE_FLAG_MARK_IN_SYNC;
        if (entries[i].directory) info->Flags |= CF_PLACEHOLDER_CREATE_FLAG_DISABLE_ON_DEMAND_POPULATION;
    }

    for (i = 0; i < count; ++i) {
        /* One entry per call, never a batch. On cldflt 10.0.26100.9549 an
         * entry whose metadata the filter stores compressed (an identity of
         * about 66 characters) leaves every later entry of the same call with
         * metadata the filter itself cannot read, and such an entry can be
         * neither repaired nor deleted while the filter is attached
         * (implementation notes (not distributed)). */
        processed = 0;
        hr = CfCreatePlaceholders(directory, &infos[i], 1, CF_CREATE_FLAG_NONE, &processed);
        /* A crash may leave a partly seeded directory. Never replace or
         * silently skip an obstruction. Resume only an exact own, clean,
         * byte-free identity, using the same proof as remote reconciliation.
         * If the batch itself failed, recheck every entry (Result alone is
         * not proof that the filter processed an entry). */
        if (FAILED(hr) || processed != 1 || FAILED(infos[i].Result)) {
            WCHAR rel[FILEES_CFAPI_MAX_PATH];
            HRESULT retry = E_INVALIDARG;
            if (swprintf_s(rel, FILEES_CFAPI_MAX_PATH, relative && relative[0] ? L"%s/%s" : L"%s",
                           relative && relative[0] ? relative : entries[i].name, entries[i].name) >= 0)
                retry = filees_remote_tree(root, rel, entries[i].identity, entries[i].identity,
                                           entries[i].size, entries[i].directory, 0);
            if (retry != S_OK) {
                filees_cfapi_fail("create_placeholders", retry);
                status = 1;
                break;
            }
        } else {
            /* Created is not yet usable: say so now, while the daemon can
             * still stop, rather than when somebody opens the file. */
            HRESULT readable = entry_readable(directory, entries[i].name);
            if (FAILED(readable)) {
                filees_cfapi_fail("placeholder_unreadable", readable);
                status = 1;
                break;
            }
        }
    }
    if (!status) {
        char detail[32];
        _snprintf_s(detail, sizeof detail, _TRUNCATE, "created %u", count);
        filees_cfapi_ok(detail);
    }
    free(entries);
    free(infos);
    return status;
}
