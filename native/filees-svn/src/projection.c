/* Private parent/child bridge for physical projection of SVN file-remove.
 * The work queue remains owned and acknowledged by SVN. No SQL, remote
 * transaction, content read, generic pathname removal or permissive retry. */
#include "filees_svn.h"
#include <stdio.h>
#include <string.h>
#include <svn_dirent_uri.h>
#ifdef _WIN32
#include <windows.h>
#include <apr_strings.h>

typedef svn_error_t *(*projection_callback)(void *, const char *, int, svn_boolean_t *, apr_pool_t *);
typedef void (*projection_install)(projection_callback, void *);
static const char *projection_token;
struct projection_baton { const char *root; unsigned sequence; };

static projection_install projection_api(void)
{
    HMODULE wc = GetModuleHandleW(L"libsvn_wc-1.dll");
    return wc ? (projection_install)GetProcAddress(wc, "svn_wc__filees_projection_v1") : NULL;
}

int filees_projection_available(void) { return projection_api() != NULL; }

svn_error_t *filees_projection_configure(const char *token)
{
    size_t i;
    if (!token || strlen(token) != 36 || projection_token)
        return filees_refuse("projection requires one operation token");
    for (i = 0; i < 36; ++i)
        if (!((token[i] >= '0' && token[i] <= '9') || (token[i] >= 'a' && token[i] <= 'f') || token[i] == '-'))
            return filees_refuse("invalid projection token");
    if (!projection_api()) return filees_refuse("projection runtime unavailable");
    projection_token = token;
    return SVN_NO_ERROR;
}

static svn_error_t *project_remove(void *baton, const char *path, int directory,
                                 svn_boolean_t *handled, apr_pool_t *pool)
{
    struct projection_baton *b = baton;
    const char *rel = svn_dirent_skip_ancestor(b->root, path);
    WCHAR *wide, *native_path;
    HANDLE file;
    BY_HANDLE_FILE_INFORMATION object;
    typedef HRESULT (WINAPI *placeholder_info_fn)(HANDLE, int, void *, DWORD, DWORD *);
    HMODULE cloud;
    placeholder_info_fn placeholder_info;
    BYTE metadata[16384];
    DWORD returned = 0;
    HRESULT cloud_hr;
    char id[64], ack[128], expected[128];
    int count;
    *handled = FALSE;
    /* Internal .svn removals keep normal SVN semantics. The public callback
     * never sees administrative paths or an escape outside the exact root. */
    if (!rel || !*rel || !strncmp(rel, ".svn/", 5)) return SVN_NO_ERROR;
    if (directory == 2) return filees_refuse("anchor projection refuses recursive directory removal");
    if (!filees_safe_relative(rel)) return filees_refuse("invalid projection work item path");
    count = MultiByteToWideChar(CP_UTF8, MB_ERR_INVALID_CHARS, path, -1, NULL, 0);
    if (!count) return filees_refuse("invalid projection UTF-8");
    wide = apr_palloc(pool, (size_t)count * sizeof *wide);
    if (!MultiByteToWideChar(CP_UTF8, MB_ERR_INVALID_CHARS, path, -1, wide, count))
        return filees_refuse("invalid projection path");
    /* Explicit extended path, independent of LongPathsEnabled on this host. */
    native_path = apr_pcalloc(pool, ((size_t)count + 8) * sizeof *native_path);
    if (count > 3 && wide[1] == L':' && (wide[2] == L'/' || wide[2] == L'\\')) {
        wcscpy_s(native_path, (size_t)count + 8, L"\\\\?\\");
        wcscat_s(native_path, (size_t)count + 8, wide);
    } else if (count > 2 && (wide[0] == L'/' || wide[0] == L'\\') && (wide[1] == L'/' || wide[1] == L'\\')) {
        wcscpy_s(native_path, (size_t)count + 8, L"\\\\?\\UNC\\");
        wcscat_s(native_path, (size_t)count + 8, wide + 2);
    } else return filees_refuse("projection requires an absolute native path");
    { WCHAR *p; for (p = native_path; *p; ++p) if (*p == L'/') *p = L'\\'; }
    file = CreateFileW(native_path, FILE_READ_ATTRIBUTES, FILE_SHARE_READ | FILE_SHARE_WRITE | FILE_SHARE_DELETE,
        NULL, OPEN_EXISTING, FILE_FLAG_OPEN_REPARSE_POINT | FILE_FLAG_BACKUP_SEMANTICS, NULL);
    if (file == INVALID_HANDLE_VALUE) {
        DWORD code = GetLastError();
        if (code == ERROR_FILE_NOT_FOUND || code == ERROR_PATH_NOT_FOUND) {
            *handled = TRUE; /* Do not unlink a subsequently created object. */
            return SVN_NO_ERROR;
        }
        return filees_refuse("cannot inspect projection work item");
    }
    if (!GetFileInformationByHandle(file, &object)) {
        CloseHandle(file);
        return filees_refuse("cannot bind projection work item");
    }
    /* Cloud Filter may virtualize Win32 reparse attributes for an ordinary
     * process. Query the documented metadata API on this very same handle;
     * an attribute bit is neither positive nor negative placeholder proof. */
    cloud = LoadLibraryExW(L"cldapi.dll", NULL, LOAD_LIBRARY_SEARCH_SYSTEM32);
    placeholder_info = cloud ? (placeholder_info_fn)GetProcAddress(cloud, "CfGetPlaceholderInfo") : NULL;
    cloud_hr = placeholder_info ? placeholder_info(file, 0, metadata, sizeof metadata, &returned) : E_FAIL;
    if (cloud) FreeLibrary(cloud);
    CloseHandle(file);
    if (directory) {
        if (!(object.dwFileAttributes & FILE_ATTRIBUTE_DIRECTORY))
            return filees_refuse("projection directory object was replaced");
        if ((object.dwFileAttributes & FILE_ATTRIBUTE_REPARSE_POINT) && FAILED(cloud_hr))
            return filees_refuse("projection refuses a foreign directory alias");
        /* CFAPI owns the visible directory skeleton. Draining a sparse SVN
         * dir-remove must retain that skeleton, not ask Explorer's remote
         * delete coordinator to remove a server folder. This performs NO
         * filesystem deletion; untracked children and local files stay put.
         * A later bound tree refresh handles confirmed remote removals. */
        *handled = TRUE;
        return SVN_NO_ERROR;
    }
    if (FAILED(cloud_hr))
        return filees_refuse(apr_psprintf(pool, "projection refuses an ordinary, replaced or unverified public file (CFAPI 0x%08lx)", (unsigned long)cloud_hr));
    if ((object.dwFileAttributes & FILE_ATTRIBUTE_DIRECTORY) || object.nNumberOfLinks != 1)
        return filees_refuse("unsupported projection object");
    sprintf_s(id, sizeof id, "%lu:%llu", (unsigned long)object.dwVolumeSerialNumber,
        ((unsigned long long)object.nFileIndexHigh << 32) | object.nFileIndexLow);
    ++b->sequence;
    printf("{\"schema\":\"filees.svn-projection/v1\",\"operation\":");
    filees_json_string(projection_token);
    printf(",\"sequence\":%u,\"root\":", b->sequence); filees_json_string(b->root);
    printf(",\"path\":"); filees_json_string(rel);
    printf(",\"file_id\":"); filees_json_string(id); puts("}"); fflush(stdout);
    apr_snprintf(expected, sizeof expected, "projection-ok %s %u\n", projection_token, b->sequence);
    if (!fgets(ack, sizeof ack, stdin) || strcmp(ack, expected))
        return filees_refuse("physical projection was not acknowledged");
    *handled = TRUE;
    return SVN_NO_ERROR;
}

svn_error_t *filees_projection_begin(const char *root, apr_pool_t *pool)
{
    struct projection_baton *b;
    if (!projection_token) return SVN_NO_ERROR;
    b = apr_pcalloc(pool, sizeof *b);
    b->root = root;
    projection_api()(project_remove, b);
    return SVN_NO_ERROR;
}
#else
int filees_projection_available(void) { return 0; }
svn_error_t *filees_projection_configure(const char *token)
{ (void)token; return filees_refuse("projection is Windows-only"); }
svn_error_t *filees_projection_begin(const char *root, apr_pool_t *pool)
{ (void)root; (void)pool; return SVN_NO_ERROR; }
#endif
