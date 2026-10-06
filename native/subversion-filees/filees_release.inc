/* FileES modification of Apache SVN 1.14.5; included by workqueue.c.
 * Private opt-in API only. Standard SVN operations retain upstream behavior.
 * Durable item: opcode, WC-relative path, guarded directory IDs, source ID,
 * full SHA-256. Never delete by pathname, including cleanup after a crash.
 * Licensed under Apache-2.0, like the containing upstream source. */
#ifdef WIN32
#include <windows.h>
#include <wincrypt.h>
#include <apr_strings.h>
#include "private/svn_wc_private.h"
#include "svn_path.h"
#pragma comment(lib, "advapi32.lib")

static svn_error_t *fr_refuse(const char *why)
{
  return svn_error_create(SVN_ERR_WC_OBSTRUCTED_UPDATE, NULL, why);
}

/* A fully hydrated cloud file can lack REPARSE_POINT on the opened handle.
 * Require an explicit ordinary-file answer, not absence of attribute flags.
 * Resolve only the system Cloud Files library; no directory/PATH probing. */
static svn_boolean_t fr_ordinary(HANDLE file)
{
  typedef HRESULT (WINAPI *get_info_fn)(HANDLE, int, void *, DWORD, DWORD *);
  HMODULE module = LoadLibraryExW(L"cldapi.dll", NULL, LOAD_LIBRARY_SEARCH_SYSTEM32);
  get_info_fn get_info;
  HRESULT hr;
  BYTE info[4096];
  DWORD returned = 0;
  if (!module) return FALSE;
  get_info = (get_info_fn)GetProcAddress(module, "CfGetPlaceholderInfo");
  hr = get_info ? get_info(file, 0, info, sizeof info, &returned) : E_FAIL;
  FreeLibrary(module);
  return hr == HRESULT_FROM_WIN32(ERROR_NOT_A_CLOUD_FILE);
}

static apr_status_t fr_close(void *baton)
{
  CloseHandle((HANDLE)baton);
  return APR_SUCCESS;
}

static const WCHAR *fr_wide(const char *path, apr_pool_t *pool)
{
  int n = MultiByteToWideChar(CP_UTF8, MB_ERR_INVALID_CHARS, path, -1, NULL, 0);
  WCHAR *w;
  if (!n || n > 32760) return NULL;
  w = apr_palloc(pool, n * sizeof(*w));
  if (!MultiByteToWideChar(CP_UTF8, MB_ERR_INVALID_CHARS, path, -1, w, n)) return NULL;
  return w;
}

static const char *fr_id(const BY_HANDLE_FILE_INFORMATION *i, apr_pool_t *pool)
{
  return apr_psprintf(pool, "%08lx:%08lx%08lx:%08lx%08lx:%08lx%08lx",
    (unsigned long)i->dwVolumeSerialNumber, (unsigned long)i->nFileIndexHigh,
    (unsigned long)i->nFileIndexLow, (unsigned long)i->ftCreationTime.dwHighDateTime,
    (unsigned long)i->ftCreationTime.dwLowDateTime, (unsigned long)i->nFileSizeHigh,
    (unsigned long)i->nFileSizeLow);
}

/* Directory sizes are not stable and carry no proof; retain ID/creation only. */
static const char *fr_dir_id(BY_HANDLE_FILE_INFORMATION i, apr_pool_t *pool)
{
  i.nFileSizeHigh = i.nFileSizeLow = 0;
  return fr_id(&i, pool);
}

/* Freeze the entire local path chain against rename/reparse changes.
 * UNC, alternate streams, dot segments and junctions are outside this ABI.
 * Only the exact WC root may have a Cloud Files reparse tag. No hydration. */
static svn_error_t *fr_parents(const char **ids, const char *path,
                              const char *wcroot, apr_pool_t *pool)
{
  const char *parent = svn_dirent_dirname(path, pool);
  char *part;
  size_t j, len = strlen(parent);
  int count = 0;
  *ids = "";
  if (len < 3 || parent[1] != ':' || parent[2] != '/' ||
      !((parent[0] >= 'A' && parent[0] <= 'Z') || (parent[0] >= 'a' && parent[0] <= 'z')))
    return fr_refuse("FileES release requires a local drive path");
  part = apr_pstrdup(pool, parent);
  for (j = 3; j <= len; ++j)
    {
      HANDLE h;
      BY_HANDLE_FILE_INFORMATION i;
      FILE_ATTRIBUTE_TAG_INFO tag;
      const WCHAR *wide;
      char saved;
      if (part[j] && part[j] != '/') continue;
      if (++count > 64) return fr_refuse("FileES release path exceeds guarded depth");
      saved = part[j]; part[j] = 0;
      wide = fr_wide(part, pool);
      if (!wide) return fr_refuse("FileES release path is not valid UTF-8");
      h = CreateFileW(wide, GENERIC_READ, FILE_SHARE_READ, NULL, OPEN_EXISTING,
                      FILE_FLAG_BACKUP_SEMANTICS | FILE_FLAG_OPEN_REPARSE_POINT, NULL);
      if (h == INVALID_HANDLE_VALUE) return fr_refuse("FileES release parent is busy or unavailable");
      apr_pool_cleanup_register(pool, h, fr_close, apr_pool_cleanup_null);
      if (!GetFileInformationByHandle(h, &i) || !(i.dwFileAttributes & FILE_ATTRIBUTE_DIRECTORY)
          || (!i.nFileIndexHigh && !i.nFileIndexLow))
        return fr_refuse("FileES release parent is not a directory");
      if (i.dwFileAttributes & FILE_ATTRIBUTE_REPARSE_POINT)
        {
          if (strcmp(part, wcroot) || !GetFileInformationByHandleEx(h, FileAttributeTagInfo, &tag, sizeof(tag))
              || (tag.ReparseTag & 0xffff0fffUL) != 0x9000001aUL)
            return fr_refuse("FileES release refuses a redirected parent");
        }
      *ids = apr_pstrcat(pool, *ids, fr_dir_id(i, pool), ";", NULL);
      part[j] = saved;
    }
  return SVN_NO_ERROR;
}

/* Attributes first: never request file bytes from a placeholder. The data
 * handle repeats admission and is the sole object used for hash/deletion. */
static svn_error_t *fr_open(HANDLE *out, const char *path, svn_boolean_t remove,
                           svn_boolean_t allow_missing, svn_boolean_t keep_copy,
                           apr_pool_t *pool)
{
  HANDLE h, data;
  BY_HANDLE_FILE_INFORMATION i, d;
  WCHAR filesystem[16];
  const WCHAR *wide = fr_wide(path, pool);
  DWORD forbidden = FILE_ATTRIBUTE_DIRECTORY | FILE_ATTRIBUTE_REPARSE_POINT |
                    FILE_ATTRIBUTE_OFFLINE | 0x00040000UL | 0x00400000UL;
  *out = INVALID_HANDLE_VALUE;
  if (!wide) return fr_refuse("FileES release path is not valid UTF-8");
  h = CreateFileW(wide, FILE_READ_ATTRIBUTES, FILE_SHARE_READ | FILE_SHARE_DELETE,
                  NULL, OPEN_EXISTING, FILE_FLAG_OPEN_REPARSE_POINT, NULL);
  if (h == INVALID_HANDLE_VALUE)
    {
      if (allow_missing && GetLastError() == ERROR_FILE_NOT_FOUND) return SVN_NO_ERROR;
      return fr_refuse("FileES release file is busy or unavailable");
    }
  if (!GetFileInformationByHandle(h, &i) || (i.dwFileAttributes & forbidden) || i.nNumberOfLinks != 1 || !fr_ordinary(h)
      || (!i.nFileIndexHigh && !i.nFileIndexLow)
      || !GetVolumeInformationByHandleW(h, NULL, 0, NULL, NULL, NULL, filesystem, 16)
      || wcscmp(filesystem, L"NTFS"))
    { CloseHandle(h); return fr_refuse("FileES release requires an ordinary single-link local file"); }
  data = CreateFileW(wide, GENERIC_READ | (remove ? DELETE | FILE_WRITE_ATTRIBUTES : 0),
                      FILE_SHARE_READ | ((remove || keep_copy) ? 0 : FILE_SHARE_DELETE),
                      NULL, OPEN_EXISTING, FILE_FLAG_OPEN_REPARSE_POINT, NULL);
  if (data == INVALID_HANDLE_VALUE)
    { CloseHandle(h); return fr_refuse("FileES release file is open for writing/deletion or unavailable"); }
  apr_pool_cleanup_register(pool, data, fr_close, apr_pool_cleanup_null);
  if (!GetFileInformationByHandle(data, &d) || (d.dwFileAttributes & forbidden) || d.nNumberOfLinks != 1 || !fr_ordinary(data)
      || strcmp(fr_id(&i, pool), fr_id(&d, pool)))
    { CloseHandle(h); return fr_refuse("FileES release file changed during admission"); }
  CloseHandle(h);
  *out = data;
  return SVN_NO_ERROR;
}

/* Repeat the publication proof UNDER the SVN write lock. Status may use
 * cached mtime/size; neither it nor the caller's earlier proof is enough. */
static svn_error_t *fr_pristine(svn_wc_context_t *ctx, const char *path, HANDLE h,
                               svn_cancel_func_t cancel_func, void *cancel_baton,
                               apr_pool_t *pool)
{
  svn_stream_t *pristine;
  unsigned char base[32768], source[32768];
  apr_size_t n;
  DWORD got;
  LARGE_INTEGER zero;
  svn_error_t *err = SVN_NO_ERROR;
  SVN_ERR(svn_wc_get_pristine_contents2(&pristine, ctx, path, pool, pool));
  if (!pristine) return fr_refuse("FileES release has no committed pristine bytes");
  zero.QuadPart = 0;
  if (!SetFilePointerEx(h, zero, NULL, FILE_BEGIN))
    err = fr_refuse("FileES release cannot rewind guarded source");
  while (!err)
    {
      if (cancel_func) err = cancel_func(cancel_baton);
      if (err) break;
      n = sizeof(base);
      err = svn_stream_read_full(pristine, (char *)base, &n);
      if (err) break;
      if (!ReadFile(h, source, sizeof(source), &got, NULL) || n != got || memcmp(base, source, n))
        { err = fr_refuse("FileES release bytes differ from committed pristine"); break; }
      if (!n) break;
    }
  return svn_error_compose_create(err, svn_stream_close(pristine));
}

static svn_error_t *fr_digest(const char **digest, HANDLE h,
                             svn_cancel_func_t cancel_func, void *cancel_baton,
                             apr_pool_t *pool)
{
  HCRYPTPROV provider = 0;
  HCRYPTHASH hash = 0;
  LARGE_INTEGER zero;
  unsigned char buffer[32768], result[32];
  DWORD n, size = sizeof(result);
  char *hex;
  int j;
  svn_error_t *err = SVN_NO_ERROR;
  zero.QuadPart = 0;
  if (!SetFilePointerEx(h, zero, NULL, FILE_BEGIN)
      || !CryptAcquireContextW(&provider, NULL, NULL, PROV_RSA_AES, CRYPT_VERIFYCONTEXT)
      || !CryptCreateHash(provider, CALG_SHA_256, 0, 0, &hash))
    err = fr_refuse("FileES release cannot initialize full-content proof");
  while (!err)
    {
      if (cancel_func) err = cancel_func(cancel_baton);
      if (err) break;
      if (!ReadFile(h, buffer, sizeof(buffer), &n, NULL))
        { err = fr_refuse("FileES release cannot read full local bytes"); break; }
      if (!n) break;
      if (!CryptHashData(hash, buffer, n, 0))
        { err = fr_refuse("FileES release cannot hash full local bytes"); break; }
    }
  if (!err && (!CryptGetHashParam(hash, HP_HASHVAL, result, &size, 0) || size != 32))
    err = fr_refuse("FileES release has no complete SHA-256 proof");
  if (hash) CryptDestroyHash(hash);
  if (provider) CryptReleaseContext(provider, 0);
  if (err) return err;
  hex = apr_palloc(pool, 65);
  for (j = 0; j < 32; ++j) sprintf(hex + 2*j, "%02x", result[j]);
  hex[64] = 0; *digest = hex;
  return SVN_NO_ERROR;
}

static svn_error_t *run_filees_remove(work_item_baton_t *wqb, svn_wc__db_t *db,
                                    const svn_skel_t *item, const char *wri_abspath,
                                    svn_cancel_func_t cancel_func, void *cancel_baton,
                                    apr_pool_t *pool)
{
  const svn_skel_t *arg = item->children->next;
  const char *values[4], *path, *root, *ids, *digest;
  BY_HANDLE_FILE_INFORMATION info;
  FILE_BASIC_INFO basic, restore;
  FILE_DISPOSITION_INFO disposition;
  HANDLE h;
  int j;
  (void)wqb;
  for (j = 0; j < 4; ++j)
    {
      if (!arg || !arg->is_atom || !arg->len || arg->len > 16384 || memchr(arg->data, 0, arg->len))
        return fr_refuse("FileES release work item is malformed");
      values[j] = apr_pstrmemdup(pool, arg->data, arg->len);
      arg = arg->next;
    }
  if (arg || !svn_relpath_is_canonical(values[0]) || !*values[0]
      || strchr(values[0], ':') || strlen(values[3]) != 64)
    return fr_refuse("FileES release work item is not a canonical file proof");
  SVN_ERR(svn_wc__db_from_relpath(&path, db, wri_abspath, values[0], pool, pool));
  SVN_ERR(svn_wc__db_get_wcroot(&root, db, wri_abspath, pool, pool));
  SVN_ERR(fr_parents(&ids, path, root, pool));
  if (strcmp(ids, values[1])) return fr_refuse("FileES release parent identity changed; no removal");
  SVN_ERR(fr_open(&h, path, TRUE, TRUE, FALSE, pool));
  if (h == INVALID_HANDLE_VALUE) return SVN_NO_ERROR; /* completed before lost ACK */
  if (!GetFileInformationByHandle(h, &info) || strcmp(fr_id(&info, pool), values[2]))
    return fr_refuse("FileES release source identity changed; no removal");
  SVN_ERR(fr_digest(&digest, h, cancel_func, cancel_baton, pool));
  if (strcmp(digest, values[3])) return fr_refuse("FileES release source bytes changed; no removal");
  if (cancel_func) SVN_ERR(cancel_func(cancel_baton));
  if (!GetFileInformationByHandleEx(h, FileBasicInfo, &basic, sizeof(basic)))
    return fr_refuse("FileES release cannot inspect guarded attributes");
  restore = basic;
  if (basic.FileAttributes & FILE_ATTRIBUTE_READONLY)
    {
      basic.FileAttributes &= ~FILE_ATTRIBUTE_READONLY;
      if (!basic.FileAttributes) basic.FileAttributes = FILE_ATTRIBUTE_NORMAL;
      if (!SetFileInformationByHandle(h, FileBasicInfo, &basic, sizeof(basic)))
        return fr_refuse("FileES release cannot change guarded read-only attribute");
    }
  disposition.DeleteFile = TRUE;
  if (!SetFileInformationByHandle(h, FileDispositionInfo, &disposition, sizeof(disposition)))
    {
      if (basic.FileAttributes != restore.FileAttributes &&
          !SetFileInformationByHandle(h, FileBasicInfo, &restore, sizeof(restore)))
        return fr_refuse("FileES release failed; guarded attribute restore also failed");
      return fr_refuse("FileES release guarded deletion failed; retained copy is required");
    }
  /* Close before acknowledging work. No pathname delete/attribute fallback. */
  apr_pool_cleanup_run(pool, h, fr_close);
  return SVN_NO_ERROR;
}

struct fr_baton {
  svn_wc_context_t *ctx;
  const char *path, *retained;
  HANDLE expected;
  svn_cancel_func_t cancel_func;
  void *cancel_baton;
  svn_revnum_t expected_revision;
  const char *expected_uuid, *expected_url;
};

static svn_error_t *fr_exclude_locked(void *baton, apr_pool_t *result_pool, apr_pool_t *pool)
{
  struct fr_baton *b = baton;
  const char *root, *rel, *ids, *copy_ids, *digest, *copy_digest;
  const char *repos_relpath, *repos_url, *repos_uuid;
  HANDLE source, retained;
  BY_HANDLE_FILE_INFORMATION expected, actual, copy;
  const svn_string_t *prop;
  svn_wc__db_status_t status;
  svn_node_kind_t kind;
  svn_revnum_t revision;
  svn_boolean_t is_root, switched, modified, deletes;
  svn_skel_t *item;
  (void)result_pool;
  SVN_ERR(svn_wc__db_get_wcroot(&root, b->ctx->db, b->path, pool, pool));
  rel = svn_dirent_skip_ancestor(root, b->path);
  if (!rel || !*rel || strchr(rel, ':') || !svn_relpath_is_canonical(rel))
    return fr_refuse("FileES release requires one canonical WC file");
  SVN_ERR(fr_parents(&ids, b->path, root, pool));
  SVN_ERR(fr_parents(&copy_ids, b->retained, "", pool));
  SVN_ERR(fr_open(&source, b->path, FALSE, FALSE, FALSE, pool));
  SVN_ERR(fr_open(&retained, b->retained, FALSE, FALSE, TRUE, pool));
  if (!GetFileInformationByHandle(b->expected, &expected) || !GetFileInformationByHandle(source, &actual)
      || strcmp(fr_id(&expected, pool), fr_id(&actual, pool)))
    return fr_refuse("FileES release verified source identity changed");
  if (!GetFileInformationByHandle(retained, &copy)
      || (actual.dwVolumeSerialNumber == copy.dwVolumeSerialNumber
          && actual.nFileIndexHigh == copy.nFileIndexHigh && actual.nFileIndexLow == copy.nFileIndexLow))
    return fr_refuse("FileES release requires an independent retained copy");
  SVN_ERR(fr_digest(&digest, source, b->cancel_func, b->cancel_baton, pool));
  SVN_ERR(fr_digest(&copy_digest, retained, b->cancel_func, b->cancel_baton, pool));
  if (strcmp(digest, copy_digest)) return fr_refuse("FileES release retained copy differs");
  SVN_ERR(svn_wc__db_is_switched(&is_root, &switched, NULL, b->ctx->db, b->path, pool));
  SVN_ERR(svn_wc__db_base_get_info(&status, &kind, &revision, &repos_relpath, &repos_url, &repos_uuid,
          NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL,
          b->ctx->db, b->path, pool, pool));
  if (is_root || switched || status != svn_wc__db_status_normal || kind != svn_node_file
      || !SVN_IS_VALID_REVNUM(revision)) return fr_refuse("FileES release requires a normal BASE file");
  if (b->expected_uuid && (revision != b->expected_revision || !repos_uuid || !repos_url || !repos_relpath
      || strcmp(repos_uuid, b->expected_uuid)
      || strcmp(svn_path_url_add_component2(repos_url, repos_relpath, pool), b->expected_url)))
    return fr_refuse("FileES release repository or revision changed under the SVN write lock");
  SVN_ERR(svn_wc__node_has_local_mods(&modified, &deletes, b->ctx->db, b->path, FALSE,
          b->cancel_func, b->cancel_baton, pool));
  if (modified) return fr_refuse("FileES release refuses local changes");
  SVN_ERR(svn_wc_prop_get2(&prop, b->ctx, b->path, "svn:special", pool, pool));
  if (prop) return fr_refuse("FileES release refuses special files");
  SVN_ERR(svn_wc_prop_get2(&prop, b->ctx, b->path, "svn:eol-style", pool, pool));
  if (prop) return fr_refuse("FileES release refuses EOL translation");
  SVN_ERR(svn_wc_prop_get2(&prop, b->ctx, b->path, "svn:keywords", pool, pool));
  if (prop) return fr_refuse("FileES release refuses keyword translation");
  SVN_ERR(fr_pristine(b->ctx, b->path, source, b->cancel_func, b->cancel_baton, pool));
  item = svn_skel__make_empty_list(pool);
  svn_skel__prepend_str(digest, item, pool);
  svn_skel__prepend_str(fr_id(&actual, pool), item, pool);
  svn_skel__prepend_str(ids, item, pool);
  svn_skel__prepend_str(rel, item, pool);
  svn_skel__prepend_str("filees-remove-verified-v1", item, pool);
  if (b->cancel_func) SVN_ERR(b->cancel_func(b->cancel_baton));
  SVN_ERR(svn_wc__db_filees_exclude(b->ctx->db, b->path, revision, item, pool));
  return svn_wc__wq_run(b->ctx->db, b->path, b->cancel_func, b->cancel_baton, pool);
}

static svn_error_t *fr_exclude(svn_wc_context_t *ctx, const char *path,
    const char *retained, void *expected, svn_cancel_func_t cancel_func,
    void *cancel_baton, svn_revnum_t revision, const char *uuid, const char *url, apr_pool_t *pool)
{
  struct fr_baton b;
  if (!svn_dirent_is_absolute(path) || !svn_dirent_is_absolute(retained) || !expected)
    return fr_refuse("FileES release requires absolute paths and a verified source handle");
  b.ctx = ctx; b.path = path; b.retained = retained; b.expected = (HANDLE)expected;
  b.cancel_func = cancel_func; b.cancel_baton = cancel_baton;
  b.expected_revision = revision; b.expected_uuid = uuid; b.expected_url = url;
  {
    apr_pool_t *guards = svn_pool_create(pool);
    svn_error_t *err = svn_wc__call_with_write_lock(fr_exclude_locked, &b, ctx, path, TRUE, guards, guards);
    svn_pool_destroy(guards);
    return err;
  }
}

svn_error_t *svn_wc__filees_exclude_retained_v1(svn_wc_context_t *ctx, const char *path,
    const char *retained, void *expected, svn_cancel_func_t cancel_func,
    void *cancel_baton, apr_pool_t *pool)
{
  return fr_exclude(ctx, path, retained, expected, cancel_func, cancel_baton,
                    SVN_INVALID_REVNUM, NULL, NULL, pool);
}

svn_error_t *svn_wc__filees_exclude_bound_v1(svn_wc_context_t *ctx, const char *path,
    const char *retained, void *expected, svn_revnum_t revision, const char *uuid,
    const char *url, svn_cancel_func_t cancel_func, void *cancel_baton, apr_pool_t *pool)
{
  if (!SVN_IS_VALID_REVNUM(revision) || !uuid || !*uuid || !url || !svn_path_is_url(url))
    return fr_refuse("FileES release requires exact repository evidence");
  return fr_exclude(ctx, path, retained, expected, cancel_func, cancel_baton, revision, uuid, url, pool);
}
#else
svn_error_t *svn_wc__filees_exclude_retained_v1(svn_wc_context_t *ctx, const char *path,
    const char *retained, void *expected, svn_cancel_func_t cancel_func,
    void *cancel_baton, apr_pool_t *pool)
{
  return svn_error_create(SVN_ERR_UNSUPPORTED_FEATURE, NULL, "FileES guarded exclusion is Windows-only");
}
#endif
