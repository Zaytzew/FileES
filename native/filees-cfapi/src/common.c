/* Output and argument helpers shared by the verbs. */
#include "filees_cfapi.h"

#include <stdio.h>
#include <string.h>

void filees_cfapi_json_string(const char *value)
{
    const unsigned char *p = (const unsigned char *)value;
    putchar('"');
    for (; *p; ++p) {
        switch (*p) {
        case '"': fputs("\\\"", stdout); break;
        case '\\': fputs("\\\\", stdout); break;
        case '\n': fputs("\\n", stdout); break;
        case '\r': fputs("\\r", stdout); break;
        case '\t': fputs("\\t", stdout); break;
        default:
            if (*p < 0x20) printf("\\u%04x", *p);
            else putchar(*p);
        }
    }
    putchar('"');
}

void filees_cfapi_ok(const char *detail)
{
    printf("{\"schema\":\"" FILEES_CFAPI_SCHEMA "\",\"ok\":true");
    if (detail && *detail) {
        fputs(",\"detail\":", stdout);
        filees_cfapi_json_string(detail);
    }
    puts("}");
    fflush(stdout);
}

/* A refusal names the step that refused and keeps the HRESULT. The daemon
 * turns both into one sentence from its own catalogue; this helper never
 * invents wording for a person to read. */
void filees_cfapi_fail(const char *code, HRESULT hr)
{
    printf("{\"schema\":\"" FILEES_CFAPI_SCHEMA "\",\"ok\":false,\"error\":");
    filees_cfapi_json_string(code);
    printf(",\"hresult\":\"0x%08lx\"}\n", (unsigned long)hr);
    fflush(stdout);
}

int filees_cfapi_widen(const char *utf8, WCHAR *out, size_t out_chars)
{
    int written;
    if (!utf8 || !out || out_chars == 0) return 0;
    written = MultiByteToWideChar(CP_UTF8, MB_ERR_INVALID_CHARS, utf8, -1, out, (int)out_chars);
    return written > 0;
}

/* Keep the wire/root spelling unchanged. Only absolute Win32 filesystem
 * calls get an extended spelling, with the same normal path resolution.
 * The process manifest alone needs a machine-wide registry policy; an
 * explicit extended path does not. Never change that policy on the host. */
static const WCHAR *filesystem_path(const WCHAR *path, WCHAR *out)
{
    WCHAR canonical[FILEES_CFAPI_MAX_PATH];
    size_t length = path ? wcsnlen(path, FILEES_CFAPI_MAX_PATH) : 0;
    DWORD count;
    if (!length || length >= FILEES_CFAPI_MAX_PATH) {
        SetLastError(ERROR_FILENAME_EXCED_RANGE);
        return NULL;
    }
    if (length < MAX_PATH) return path; /* retain every short-path behavior */
    if (path[0] == L'\\' && path[1] == L'\\' && path[2] == L'?' && path[3] == L'\\') return path;
    if ((path[0] == L'\\' && path[1] == L'\\' && path[2] != L'.') ||
        (((path[0] >= L'A' && path[0] <= L'Z') || (path[0] >= L'a' && path[0] <= L'z')) &&
         path[1] == L':' && (path[2] == L'\\' || path[2] == L'/'))) {
        count = GetFullPathNameW(path, FILEES_CFAPI_MAX_PATH, canonical, NULL);
        if (!count) return NULL;
        if (count >= FILEES_CFAPI_MAX_PATH) {
            SetLastError(ERROR_FILENAME_EXCED_RANGE);
            return NULL;
        }
        if (canonical[0] == L'\\' && canonical[1] == L'\\')
            count = swprintf_s(out, FILEES_CFAPI_MAX_PATH + 8, L"\\\\?\\UNC\\%s", canonical + 2);
        else count = swprintf_s(out, FILEES_CFAPI_MAX_PATH + 8, L"\\\\?\\%s", canonical);
        if ((int)count >= 0) return out;
    }
    SetLastError(ERROR_INVALID_NAME);
    return NULL;
}

HANDLE filees_cfapi_open_path(const WCHAR *path, DWORD access, DWORD share,
    LPSECURITY_ATTRIBUTES security, DWORD disposition, DWORD flags, HANDLE template_file)
{
    WCHAR extended[FILEES_CFAPI_MAX_PATH + 8];
    const WCHAR *name = filesystem_path(path, extended);
    if (!name) return INVALID_HANDLE_VALUE;
    return CreateFileW(name, access, share, security, disposition, flags, template_file);
}

HANDLE filees_cfapi_find_first(const WCHAR *path, WIN32_FIND_DATAW *data)
{
    WCHAR extended[FILEES_CFAPI_MAX_PATH + 8];
    const WCHAR *name = filesystem_path(path, extended);
    if (!name) return INVALID_HANDLE_VALUE;
    return FindFirstFileW(name, data);
}

DWORD filees_cfapi_path_attributes(const WCHAR *path)
{
    WCHAR extended[FILEES_CFAPI_MAX_PATH + 8];
    const WCHAR *name = filesystem_path(path, extended);
    if (!name) return INVALID_FILE_ATTRIBUTES;
    return GetFileAttributesW(name);
}

HRESULT filees_cfapi_open_oplock(const WCHAR *path, CF_OPEN_FILE_FLAGS flags, HANDLE *handle)
{
    WCHAR extended[FILEES_CFAPI_MAX_PATH + 8];
    const WCHAR *name = filesystem_path(path, extended);
    if (!name) return HRESULT_FROM_WIN32(GetLastError());
    return CfOpenFileWithOplock(name, flags, handle);
}
