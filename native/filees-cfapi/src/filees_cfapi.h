/* FileES Explorer anchor helper. Windows Cloud Files API only.
 *
 * The daemon owns Subversion; this helper owns the shell. It registers one
 * anchor folder as a sync root, seeds it with placeholders named after HEAD,
 * and answers the filter's callbacks. It never speaks to a server and never
 * touches a working copy: everything it knows arrives on argv and stdin.
 */
#ifndef FILEES_CFAPI_H
#define FILEES_CFAPI_H

#include <windows.h>
#include <cfapi.h>

#ifdef __cplusplus
extern "C" {
#endif

#define FILEES_CFAPI_SCHEMA "filees.cfapi/v1"
#ifndef FILEES_CFAPI_VERSION
#error "FILEES_CFAPI_VERSION must be supplied by the build"
#endif
#define FILEES_CFAPI_PROVIDER L"FileES"
/* One identity blob per anchor: "<server id>\0<repo id>", as the daemon sends
 * it. The filter hands it back on every callback, so the helper never has to
 * guess which anchor a request belongs to. */
#define FILEES_CFAPI_MAX_IDENTITY 512
/* A listing arrives on stdin, one entry per line, and is bounded so a
 * malformed producer cannot make this process grow without limit. */
#define FILEES_CFAPI_MAX_ENTRIES 4096
/* Long paths are ordinary in a project tree; MAX_PATH is the shell's old
 * limit, not the file system's. */
#define FILEES_CFAPI_MAX_PATH 1024
#define FILEES_CFAPI_MAX_LINE 4096

/* One listing entry: what Explorer shows before anything is downloaded. */
struct filees_entry {
    WCHAR name[MAX_PATH];
    WCHAR identity[FILEES_CFAPI_MAX_IDENTITY];
    LONGLONG size;
    int directory;
};

/* JSON on stdout, one object, always. Callers parse this, not exit codes
 * alone. */
void filees_cfapi_ok(const char *detail);
void filees_cfapi_fail(const char *code, HRESULT hr);
void filees_cfapi_json_string(const char *value);

/* Optional metadata-only stderr evidence. No registration repair or authority. */
int filees_registration_trace_enabled(void);
void filees_registration_trace(const WCHAR *root, const char *operation, const char *phase,
    HRESULT result, const WCHAR *identity);
const char *filees_shell_diagnostic(const WCHAR *root);

/* UTF-8 argv into wide strings; the shell is wide everywhere. */
int filees_cfapi_widen(const char *utf8, WCHAR *out, size_t out_chars);

/* Filesystem-only spelling, independent of the machine's LongPathsEnabled.
 * Never changes identities, bridge paths, access/share flags or root proof. */
HANDLE filees_cfapi_open_path(const WCHAR *path, DWORD access, DWORD share,
    LPSECURITY_ATTRIBUTES security, DWORD disposition, DWORD flags, HANDLE template_file);
HANDLE filees_cfapi_find_first(const WCHAR *path, WIN32_FIND_DATAW *data);
DWORD filees_cfapi_path_attributes(const WCHAR *path);
HRESULT filees_cfapi_open_oplock(const WCHAR *path, CF_OPEN_FILE_FLAGS flags, HANDLE *handle);

/* The call to the daemon (bridge.c). waiting() is invoked about once a second
 * while an answer has not arrived; returning 0 abandons the request. */
typedef int (*filees_bridge_waiting)(void *context);

void filees_bridge_start(void);
void filees_bridge_answer(char *line);
void filees_bridge_say_reverted(const char *id, HRESULT hr);
void filees_bridge_say_state_set(const char *id, HRESULT hr);
void filees_bridge_say_stream_set(const char *id, HRESULT hr);
void filees_bridge_say_release_prepared(const char *id, HRESULT hr);
void filees_bridge_say_release_restored(const char *id, HRESULT hr);
void filees_bridge_say_tree_set(const char *id, HRESULT hr);
void filees_bridge_say_tree_move(const char *id, HRESULT hr);
void filees_bridge_say_tree_identity(const char *id, HRESULT hr);
void filees_remote_rename_begin(DWORD volume, ULONGLONG file_id);
void filees_remote_rename_end(void);
void filees_remote_delete_begin(DWORD volume, ULONGLONG file_id);
void filees_remote_delete_end(void);
int filees_bridge_request(const WCHAR *identity, const WCHAR *process, LONGLONG offset, LONGLONG length,
                          filees_bridge_waiting waiting, void *context, WCHAR *path, int *id_out);
void filees_bridge_say_hydrated(int id, const WCHAR *identity);
void filees_bridge_say_fetched(int id, const WCHAR *identity);

#ifdef FILEES_CFAPI_DELETE_PROBE
/* Probe-only serialized diagnostic line, not a product approval protocol. */
int filees_bridge_say_delete_probe(const char *phase, DWORD volume,
    LONGLONG root_id, LONGLONG file_id, LONGLONG request, DWORD flags,
    const void *identity, DWORD identity_length, const WCHAR *path, DWORD process);
#endif
/* Typed handshake: only a bound daemon intent can grant external deletion. */
int filees_bridge_delete_request(DWORD volume, LONGLONG root_id, LONGLONG file_id,
    LONGLONG request, DWORD flags, const void *identity, DWORD identity_length,
    const WCHAR *path, DWORD process, int *id_out);
void filees_bridge_say_delete_acked(int id, HRESULT hr);
void filees_bridge_say_delete_complete(DWORD volume, LONGLONG root_id, LONGLONG file_id,
    LONGLONG request, DWORD flags, const void *identity, DWORD identity_length,
    const WCHAR *path, DWORD process);

/* Verbs. Each returns a process exit code and has already printed its JSON. */
int filees_bridge_rename_request(const CF_CALLBACK_INFO *info, DWORD flags, const WCHAR *target, int *id_out);
void filees_bridge_say_rename_acked(int id, HRESULT hr);
void filees_bridge_say_rename_complete(const CF_CALLBACK_INFO *info, DWORD flags, const WCHAR *source);
int filees_cfapi_register(const WCHAR *root, const WCHAR *identity);
int filees_cfapi_unregister(const WCHAR *root);
int filees_cfapi_info(const WCHAR *root);
int filees_cfapi_revert(const WCHAR *path);
HRESULT filees_revert_placeholder(const WCHAR *path);
int filees_cfapi_placeholders(const WCHAR *root, const WCHAR *relative);
int filees_cfapi_connect(const WCHAR *root);

/* Windows Runtime is isolated behind this C ABI; no network or SVN access. */
int filees_cfapi_shell_register(const WCHAR *root, const WCHAR *identity, const WCHAR *icon, BOOL visible);
int filees_cfapi_shell_visibility(const WCHAR *root, BOOL visible);
HRESULT filees_shell_unregister(const WCHAR *root);
int filees_cfapi_shell_hide_obsolete(const WCHAR *root, const WCHAR *registration_id);
HRESULT filees_own_sync_root(const WCHAR *root);

/* Availability does not prove publication. SVN is checked between a read-only
 * USN probe and a compare-and-set sent to the connected provider. */
int filees_cfapi_file_state(const WCHAR *root, const WCHAR *relative);
int filees_cfapi_hydrate(const WCHAR *root, const WCHAR *relative);
int filees_cfapi_file_states(const WCHAR *root);
int filees_cfapi_tree_states(const WCHAR *root);
HRESULT filees_set_directory_state(const WCHAR *root, const WCHAR *relative,
    const WCHAR *identity, LONGLONG expected_usn, int clean, char **lines, unsigned input_count);
void filees_bridge_say_directory_set(const char *id, HRESULT hr);
HRESULT filees_set_local_state(const WCHAR *root, const WCHAR *relative, LONGLONG expected_usn, int clean);
int filees_cfapi_verify_download(const WCHAR *root, const WCHAR *relative, const WCHAR *identity,
    const char *expected_id, LONGLONG size, LONGLONG usn, const char *digest);
HRESULT filees_ack_remote_streams(const WCHAR *root, const WCHAR *relative, const char *expected_id, const WCHAR *identity, LONGLONG size, LONGLONG expected_usn);
HRESULT filees_prepare_release(const WCHAR *root, const WCHAR *relative, LONGLONG expected_usn);
HRESULT filees_prepare_release_bound(const WCHAR *root, const WCHAR *relative, LONGLONG expected_usn, const char *expected_id);
HRESULT filees_prepare_release_verified(const WCHAR *root, const WCHAR *relative, LONGLONG expected_usn,
                                       const char *expected_id, LONGLONG expected_size, const char *expected_digest);
HRESULT filees_restore_release(const WCHAR *root, const WCHAR *relative, LONGLONG size, const WCHAR *remote_identity);
int filees_local_identity(const void *identity, DWORD length);
HRESULT filees_remote_tree(const WCHAR *root, const WCHAR *relative, const WCHAR *previous,
                           const WCHAR *identity, LONGLONG size, int directory, int remove);
HRESULT filees_recovery_remove(const WCHAR *root, const WCHAR *relative,
    const WCHAR *identity, LONGLONG size, const char *expected_id, LONGLONG expected_usn);
/* Connected-provider-only, exact FileID. Physical directory move and identity
 * retarget never read file data; coordinator owns all descendants and receipt. */
HRESULT filees_move_tree(const WCHAR *root, const WCHAR *source, const WCHAR *destination, const char *expected_id);
HRESULT filees_move_identity(const WCHAR *root, const WCHAR *relative, const char *expected_id,
                             const WCHAR *previous, const WCHAR *identity);

#ifdef __cplusplus
}
#endif

#endif
