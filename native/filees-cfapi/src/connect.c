/* Connecting to one registered anchor and answering the filter.
 *
 * Registration survives a reboot; this connection does not. The daemon starts
 * this verb and keeps it running: the helper holds the connection until its
 * stdin closes, which is how the daemon's own exit takes the anchor offline
 * without a second channel.
 *
 * A click on a placeholder suspends CreateFile inside Windows until this
 * process answers. The answer is not a download: the daemon materializes that
 * one path into the working copy (sparse update), and the bytes handed back
 * are read from that file. TRANSFER_DATA is the bridge for the handle
 * Windows is holding open, not a second copy of the file.
 */
#include "filees_cfapi.h"

#include <cfapi.h>
#include <stdio.h>
#include <string.h>
#include <errno.h>

/* New release requests have canonical unsigned decimal tokens only. Refuse
 * signs, whitespace, suffixes and saturation instead of interpreting them. */
static int release_number(const char *token, LONGLONG *value, int zero_allowed)
{
    const char *p;
    char *end;
    if (!token || !*token) return 0;
    for (p = token; *p; ++p) if (*p < '0' || *p > '9') return 0;
    errno = 0;
    *value = _strtoi64(token, &end, 10);
    return errno != ERANGE && !*end && *value >= (zero_allowed ? 0 : 1);
}

/* The SDK documents CF_SIZE_OF_OP_PARAM but does not define it in cfapi.h.
 * The filter reads exactly as many bytes as the answered member needs, so the
 * size is the member's offset plus its own size - computed here rather than
 * guessed at with sizeof(the whole union). */
#define FILEES_OP_PARAM_SIZE(field)     (ULONG)(FIELD_OFFSET(CF_OPERATION_PARAMETERS, field) + sizeof(((CF_OPERATION_PARAMETERS *)0)->field))

#ifndef STATUS_SUCCESS
#define STATUS_SUCCESS ((NTSTATUS)0x00000000L)
#endif
#ifndef STATUS_UNSUCCESSFUL
#define STATUS_UNSUCCESSFUL ((NTSTATUS)0xC0000001L)
#endif
#ifndef STATUS_ACCESS_DENIED
#define STATUS_ACCESS_DENIED ((NTSTATUS)0xC0000022L)
#endif
/* Measured on the owner's machine 2026-09-23: refusing a delete with the
 * generic access-denied status made Explorer say "the cloud operation was
 * unsuccessful", which reads like a failure. The cloud-specific status reads
 * as what it is: access to this cloud file is denied. */
#ifndef STATUS_CLOUD_FILE_ACCESS_DENIED
#define STATUS_CLOUD_FILE_ACCESS_DENIED ((NTSTATUS)0xC000CF18L)
#endif

/* One megabyte per CfExecute: large enough that a drawing does not turn into
 * thousands of round trips, small enough to keep progress moving. A transfer
 * length must be a multiple of 4096 unless it ends at the end of the file. */
#define FILEES_CHUNK (1024 * 1024)
/* A fetch the daemon never answers must end. Twenty minutes is far longer
 * than any materialization this is meant for and still finite, because a
 * callback that never answers leaves Explorer hanging for good. */
#define FILEES_FETCH_TIMEOUT_TICKS (20 * 60)

static CF_CONNECTION_KEY g_connection;
static const WCHAR *g_root;
static int g_trace;

/* Callbacks run where nobody can see them. FILEES_CFAPI_TRACE=1 makes them
 * say what arrived and what was answered; without it the helper stays silent
 * on stderr, because the daemon reads that stream. */
static void trace(const char *what, HRESULT hr)
{
    if (!g_trace) return;
    fprintf(stderr, "%s hr=0x%08lx\n", what, (unsigned long)hr);
    fflush(stderr);
}

static void operation_info(const CF_CALLBACK_INFO *info, CF_OPERATION_TYPE type, CF_OPERATION_INFO *out)
{
    ZeroMemory(out, sizeof *out);
    out->StructSize = sizeof *out;
    out->Type = type;
    out->ConnectionKey = info->ConnectionKey;
    out->TransferKey = info->TransferKey;
    out->CorrelationVector = info->CorrelationVector;
    out->RequestKey = info->RequestKey;
}

static void fail_fetch(const CF_CALLBACK_INFO *info, LARGE_INTEGER offset, LARGE_INTEGER length, NTSTATUS status)
{
    CF_OPERATION_INFO operation;
    CF_OPERATION_PARAMETERS answer;
    operation_info(info, CF_OPERATION_TYPE_TRANSFER_DATA, &operation);
    ZeroMemory(&answer, sizeof answer);
    answer.ParamSize = FILEES_OP_PARAM_SIZE(TransferData);
    answer.TransferData.Flags = CF_OPERATION_TRANSFER_DATA_FLAG_NONE;
    answer.TransferData.CompletionStatus = status;
    answer.TransferData.Buffer = NULL;
    answer.TransferData.Offset = offset;
    answer.TransferData.Length = length;
    trace("fetch failed", CfExecute(&operation, &answer));
}

struct progress {
    const CF_CALLBACK_INFO *info;
    LONGLONG total;
    int ticks;
};

/* Called about once a second while the daemon works. Explorer shows the
 * provider's progress, so a long sparse update reads as "downloading", not as
 * an application that stopped responding. */
static int still_waiting(void *context)
{
    struct progress *state = context;
    LARGE_INTEGER total, done;
    total.QuadPart = state->total;
    done.QuadPart = 0;
    CfReportProviderProgress(state->info->ConnectionKey, state->info->TransferKey, total, done);
    return ++state->ticks < FILEES_FETCH_TIMEOUT_TICKS;
}

/* Hand over one file, in chunks, from the working copy. Offsets are the
 * filter's, not the file's: Windows asks for the range it needs. */
static int transfer_file(const CF_CALLBACK_INFO *info, const WCHAR *path,
                         LARGE_INTEGER offset, LARGE_INTEGER length)
{
    HANDLE file;
    BYTE *buffer;
    LONGLONG position = offset.QuadPart, left = length.QuadPart;
    LARGE_INTEGER done;
    int ok = 1;

    file = CreateFileW(path, GENERIC_READ, FILE_SHARE_READ | FILE_SHARE_WRITE | FILE_SHARE_DELETE,
                       NULL, OPEN_EXISTING, FILE_ATTRIBUTE_NORMAL, NULL);
    if (file == INVALID_HANDLE_VALUE) return 0;
    buffer = VirtualAlloc(NULL, FILEES_CHUNK, MEM_COMMIT, PAGE_READWRITE);
    if (!buffer) {
        CloseHandle(file);
        return 0;
    }

    while (left > 0) {
        CF_OPERATION_INFO operation;
        CF_OPERATION_PARAMETERS answer;
        LARGE_INTEGER seek;
        DWORD wanted = (DWORD)(left < FILEES_CHUNK ? left : FILEES_CHUNK), read = 0;
        HRESULT hr;

        seek.QuadPart = position;
        if (!SetFilePointerEx(file, seek, NULL, FILE_BEGIN) || !ReadFile(file, buffer, wanted, &read, NULL) || read == 0) {
            ok = 0;
            break;
        }
        operation_info(info, CF_OPERATION_TYPE_TRANSFER_DATA, &operation);
        ZeroMemory(&answer, sizeof answer);
        answer.ParamSize = FILEES_OP_PARAM_SIZE(TransferData);
        answer.TransferData.Flags = CF_OPERATION_TRANSFER_DATA_FLAG_NONE;
        answer.TransferData.CompletionStatus = STATUS_SUCCESS;
        answer.TransferData.Buffer = buffer;
        answer.TransferData.Offset.QuadPart = position;
        answer.TransferData.Length.QuadPart = read;
        hr = CfExecute(&operation, &answer);
        trace("fetch chunk", hr);
        if (FAILED(hr)) {
            ok = 0;
            break;
        }
        position += read;
        left -= read;
        done.QuadPart = length.QuadPart - left;
        CfReportProviderProgress(info->ConnectionKey, info->TransferKey, length, done);
    }

    VirtualFree(buffer, 0, MEM_RELEASE);
    CloseHandle(file);
    return ok;
}

/* Every callback runs on a thread pool thread, several at a time, and none of
 * them returns a value: the answer is always a CfExecute. */
static void CALLBACK on_fetch_data(const CF_CALLBACK_INFO *info, const CF_CALLBACK_PARAMETERS *params)
{
    WCHAR path[FILEES_CFAPI_MAX_PATH];
    struct progress state;
    const WCHAR *process;
    int id = 0;
    LARGE_INTEGER offset = params->FetchData.RequiredFileOffset;
    LARGE_INTEGER length = params->FetchData.RequiredLength;

    if (!info->FileIdentity || info->FileIdentityLength < sizeof(WCHAR)) {
        fail_fetch(info, offset, length, STATUS_UNSUCCESSFUL);
        return;
    }
    state.info = info;
    state.total = length.QuadPart;
    state.ticks = 0;
    /* The identity is what the placeholder was created with: the path inside
     * the repository. The daemon turns it into a path on this disk, which is
     * the only thing this process is allowed to read. */
    process = info->ProcessInfo ? info->ProcessInfo->ImagePath : NULL;
    if (!filees_bridge_request((const WCHAR *)info->FileIdentity, process, offset.QuadPart, length.QuadPart,
                               still_waiting, &state, path, &id)) {
        fail_fetch(info, offset, length, STATUS_UNSUCCESSFUL);
        return;
    }
    if (!transfer_file(info, path, offset, length)) {
        fail_fetch(info, offset, length, STATUS_UNSUCCESSFUL);
        return;
    }
    /* Only a range that reaches the end of the file completes it. A reader
     * that asked for the first page alone leaves the rest for later, and the
     * daemon must not let Subversion read a file that is not whole yet. */
    if (offset.QuadPart + length.QuadPart >= info->FileSize.QuadPart)
        filees_bridge_say_hydrated(id, (const WCHAR *)info->FileIdentity);
}

static void CALLBACK on_cancel_fetch_data(const CF_CALLBACK_INFO *info, const CF_CALLBACK_PARAMETERS *params)
{
    /* Windows has given up on this range. The request in flight will finish
     * on its own and its answer will be dropped, because the slot waiting for
     * it is gone (bridge.c). Nothing here needs to be cancelled by hand. */
    (void)info;
    (void)params;
    trace("fetch cancelled", S_OK);
}

/* Unread/unadopted seeds cannot be deleted or renamed through Explorer.
 * Retained ALWAYS_FULL local files are ordinary WC work: pass the operation
 * to the existing watcher/publication policy, including replace-on-save. */
/* A metadata-only removal is admitted solely for the connected provider's
 * own process and the exact open object. Never relax Explorer's policy. The
 * reader executes one command at a time; callbacks read this short-lived
 * binding under a separate lock (no lock is held across the delete). */
static SRWLOCK g_delete_lock = SRWLOCK_INIT;
static DWORD g_delete_volume;
static ULONGLONG g_delete_file;
static int g_delete_active;

void filees_remote_delete_begin(DWORD volume, ULONGLONG file_id)
{
    AcquireSRWLockExclusive(&g_delete_lock);
    g_delete_volume = volume; g_delete_file = file_id; g_delete_active = 1;
    ReleaseSRWLockExclusive(&g_delete_lock);
}

void filees_remote_delete_end(void)
{
    AcquireSRWLockExclusive(&g_delete_lock);
    g_delete_active = 0;
    ReleaseSRWLockExclusive(&g_delete_lock);
}

static int own_remote_delete(const CF_CALLBACK_INFO *info)
{
    int allowed;
    AcquireSRWLockShared(&g_delete_lock);
    allowed = g_delete_active && info->ProcessInfo &&
        info->ProcessInfo->ProcessId == GetCurrentProcessId() &&
        info->VolumeSerialNumber == g_delete_volume &&
        (ULONGLONG)info->FileId.QuadPart == g_delete_file;
    ReleaseSRWLockShared(&g_delete_lock);
    return allowed;
}

static void CALLBACK on_delete(const CF_CALLBACK_INFO *info, const CF_CALLBACK_PARAMETERS *params)
{
    CF_OPERATION_INFO operation;
    CF_OPERATION_PARAMETERS answer;
    HRESULT hr;
    int delete_id = 0;
    (void)params;
    operation_info(info, CF_OPERATION_TYPE_ACK_DELETE, &operation);
    ZeroMemory(&answer, sizeof answer);
    answer.ParamSize = FILEES_OP_PARAM_SIZE(AckDelete);
    answer.AckDelete.Flags = CF_OPERATION_ACK_DELETE_FLAG_NONE;
    answer.AckDelete.CompletionStatus = (filees_local_identity(info->FileIdentity, info->FileIdentityLength) || own_remote_delete(info))
        ? STATUS_SUCCESS : STATUS_CLOUD_FILE_ACCESS_DENIED;
#ifdef FILEES_CFAPI_DELETE_PROBE
    /* Characterization only, in a differently named, opt-in executable. The
     * regular provider has neither this environment switch nor this override. */
    if (GetEnvironmentVariableW(L"FILEES_CFAPI_DELETE_PROBE_BRIDGE", NULL, 0) > 0) {
#else
    {
#endif
        /* Local files and the provider's own projection keep their existing
         * gate; they cannot create an external deletion intent. */
        if (answer.AckDelete.CompletionStatus != STATUS_SUCCESS &&
            filees_bridge_delete_request(info->VolumeSerialNumber,
                info->SyncRootFileId.QuadPart, info->FileId.QuadPart, info->RequestKey.QuadPart,
                params->Delete.Flags, info->FileIdentity, info->FileIdentityLength,
                info->NormalizedPath, info->ProcessInfo ? info->ProcessInfo->ProcessId : 0, &delete_id))
            answer.AckDelete.CompletionStatus = STATUS_SUCCESS;
    }
#ifdef FILEES_CFAPI_DELETE_PROBE
    else if (filees_bridge_say_delete_probe("pre", info->VolumeSerialNumber,
        info->SyncRootFileId.QuadPart, info->FileId.QuadPart, info->RequestKey.QuadPart,
        params->Delete.Flags, info->FileIdentity, info->FileIdentityLength,
        info->NormalizedPath, info->ProcessInfo ? info->ProcessInfo->ProcessId : 0) &&
        GetEnvironmentVariableW(L"FILEES_CFAPI_DELETE_PROBE_ALLOW", NULL, 0) > 0)
        answer.AckDelete.CompletionStatus = STATUS_SUCCESS;
#endif
    hr = CfExecute(&operation, &answer);
    if (delete_id) filees_bridge_say_delete_acked(delete_id, hr);
    trace("delete acknowledgement", hr);
}

static void CALLBACK on_delete_completion(const CF_CALLBACK_INFO *info, const CF_CALLBACK_PARAMETERS *params)
{
#ifdef FILEES_CFAPI_DELETE_PROBE
    if (GetEnvironmentVariableW(L"FILEES_CFAPI_DELETE_PROBE_BRIDGE", NULL, 0) > 0)
#endif
        filees_bridge_say_delete_complete(info->VolumeSerialNumber,
            info->SyncRootFileId.QuadPart, info->FileId.QuadPart, info->RequestKey.QuadPart,
            params->DeleteCompletion.Flags, info->FileIdentity, info->FileIdentityLength,
            info->NormalizedPath, info->ProcessInfo ? info->ProcessInfo->ProcessId : 0);
#ifdef FILEES_CFAPI_DELETE_PROBE
    else filees_bridge_say_delete_probe("complete", info->VolumeSerialNumber,
        info->SyncRootFileId.QuadPart, info->FileId.QuadPart, info->RequestKey.QuadPart,
        params->DeleteCompletion.Flags, info->FileIdentity, info->FileIdentityLength,
        info->NormalizedPath, info->ProcessInfo ? info->ProcessInfo->ProcessId : 0);
#endif
}

static SRWLOCK g_rename_lock = SRWLOCK_INIT;
static DWORD g_rename_volume;
static ULONGLONG g_rename_file;
static int g_rename_active;

void filees_remote_rename_begin(DWORD volume, ULONGLONG file_id)
{
    AcquireSRWLockExclusive(&g_rename_lock);
    g_rename_volume = volume; g_rename_file = file_id; g_rename_active = 1;
    ReleaseSRWLockExclusive(&g_rename_lock);
}

void filees_remote_rename_end(void)
{
    AcquireSRWLockExclusive(&g_rename_lock);
    g_rename_active = 0;
    ReleaseSRWLockExclusive(&g_rename_lock);
}

static int own_remote_rename(const CF_CALLBACK_INFO *info)
{
    int allowed;
    AcquireSRWLockShared(&g_rename_lock);
    allowed = g_rename_active && info->ProcessInfo &&
        info->ProcessInfo->ProcessId == GetCurrentProcessId() &&
        info->VolumeSerialNumber == g_rename_volume &&
        (ULONGLONG)info->FileId.QuadPart == g_rename_file;
    ReleaseSRWLockShared(&g_rename_lock);
    return allowed;
}

static void CALLBACK on_rename(const CF_CALLBACK_INFO *info, const CF_CALLBACK_PARAMETERS *params)
{
    CF_OPERATION_INFO operation;
    CF_OPERATION_PARAMETERS answer;
    HRESULT hr;
    int rename_id = 0;
    operation_info(info, CF_OPERATION_TYPE_ACK_RENAME, &operation);
    ZeroMemory(&answer, sizeof answer);
    answer.ParamSize = FILEES_OP_PARAM_SIZE(AckRename);
    answer.AckRename.Flags = CF_OPERATION_ACK_RENAME_FLAG_NONE;
    answer.AckRename.CompletionStatus = (filees_local_identity(info->FileIdentity, info->FileIdentityLength) || own_remote_rename(info))
        ? STATUS_SUCCESS : STATUS_CLOUD_FILE_ACCESS_DENIED;
    if (answer.AckRename.CompletionStatus != STATUS_SUCCESS &&
        filees_bridge_rename_request(info, params->Rename.Flags, params->Rename.TargetPath, &rename_id))
        answer.AckRename.CompletionStatus = STATUS_SUCCESS;
    hr = CfExecute(&operation, &answer);
    if (rename_id) filees_bridge_say_rename_acked(rename_id,
        answer.AckRename.CompletionStatus == STATUS_SUCCESS ? hr : E_ACCESSDENIED);
    trace("rename acknowledgement", hr);
}

static void CALLBACK on_rename_completion(const CF_CALLBACK_INFO *info, const CF_CALLBACK_PARAMETERS *params)
{
    filees_bridge_say_rename_complete(info, params->RenameCompletion.Flags, params->RenameCompletion.SourcePath);
}

static CF_CALLBACK_REGISTRATION k_callbacks[] = {
    {CF_CALLBACK_TYPE_FETCH_DATA, on_fetch_data},
    {CF_CALLBACK_TYPE_CANCEL_FETCH_DATA, on_cancel_fetch_data},
    {CF_CALLBACK_TYPE_NOTIFY_DELETE, on_delete},
    {CF_CALLBACK_TYPE_NOTIFY_DELETE_COMPLETION, on_delete_completion},
    {CF_CALLBACK_TYPE_NOTIFY_RENAME, on_rename},
    {CF_CALLBACK_TYPE_NOTIFY_RENAME_COMPLETION, on_rename_completion},
    CF_CALLBACK_REGISTRATION_END
};

/* revert <TAB> id <TAB> absolute path, from the daemon once Subversion has
 * adopted a materialized file. Only the connected provider may do this; the
 * answer is `reverted <TAB> id <TAB> 0x...` on the same stdout the fetch
 * requests use, so the daemon reads one stream. A file an application holds
 * open is refused, and the daemon asks again later. */
static void revert_request(char *rest)
{
    char *path = strchr(rest, '\t');
    WCHAR wide[FILEES_CFAPI_MAX_PATH];
    HRESULT hr = E_INVALIDARG;
    if (path) {
        *path++ = '\0';
        if (MultiByteToWideChar(CP_UTF8, MB_ERR_INVALID_CHARS, path, -1, wide, FILEES_CFAPI_MAX_PATH) > 0)
            hr = filees_revert_placeholder(wide);
    }
    trace("revert", hr);
    filees_bridge_say_reverted(rest, hr);
}

int filees_cfapi_connect(const WCHAR *root)
{
    char line[FILEES_CFAPI_MAX_LINE];
    HRESULT hr;
    int protocol_error = 0;

    g_root = root;
    g_trace = GetEnvironmentVariableW(L"FILEES_CFAPI_TRACE", NULL, 0) > 0;
    filees_bridge_start();
    hr = CfConnectSyncRoot(root, k_callbacks, NULL,
                           CF_CONNECT_FLAG_REQUIRE_PROCESS_INFO | CF_CONNECT_FLAG_REQUIRE_FULL_FILE_PATH,
                           &g_connection);
    if (FAILED(hr)) {
        filees_cfapi_fail("connect_sync_root", hr);
        return 1;
    }
    /* Connecting is not enough: until the provider says it is idle, the
     * platform treats it as not ready and refuses hydration itself, without
     * ever calling FETCH_DATA. That looks exactly like a broken callback
     * table, which is the wrong place to go looking. */
    hr = CfUpdateSyncProviderStatus(g_connection, CF_PROVIDER_STATUS_IDLE);
    if (FAILED(hr)) {
        CfDisconnectSyncRoot(g_connection);
        filees_cfapi_fail("provider_status", hr);
        return 1;
    }

    /* The caller learns the anchor is live before anything is read from
     * stdin, so a daemon waiting for this line is never waiting on a click. */
    filees_cfapi_ok("connected");

    /* From here this thread is the reader: every line is an answer for a
     * callback waiting on it. Closed stdin, which includes the daemon
     * exiting, ends the anchor. */
    while (fgets(line, sizeof line, stdin)) {
        size_t length = strlen(line);
        while (length && (line[length - 1] == '\n' || line[length - 1] == '\r')) line[--length] = '\0';
        if (!length) continue;
        if (!strncmp(line, "tree-move\t", 10) || !strncmp(line, "tree-identity\t", 14)) {
            int retarget = !strncmp(line, "tree-identity\t", 14), i, count = retarget ? 5 : 4;
            char *parts[5], *next = line + (retarget ? 14 : 10);
            WCHAR first[FILEES_CFAPI_MAX_PATH], second[FILEES_CFAPI_MAX_PATH], third[FILEES_CFAPI_MAX_IDENTITY];
            hr = E_INVALIDARG;
            for (i = 0; i < count && next; ++i) {
                parts[i] = next; next = strchr(next, '\t'); if (next) *next++ = 0;
            }
            if (i == count && !next && *parts[0] &&
                filees_cfapi_widen(parts[1], first, FILEES_CFAPI_MAX_PATH)) {
                if (retarget && filees_cfapi_widen(parts[3], second, FILEES_CFAPI_MAX_PATH) &&
                    filees_cfapi_widen(parts[4], third, FILEES_CFAPI_MAX_IDENTITY))
                    hr = filees_move_identity(g_root, first, parts[2], second, third);
                else if (!retarget && filees_cfapi_widen(parts[2], second, FILEES_CFAPI_MAX_PATH))
                    hr = filees_move_tree(g_root, first, second, parts[3]);
            }
            if (retarget) filees_bridge_say_tree_identity(line + 14, hr);
            else filees_bridge_say_tree_move(line + 10, hr);
        }
        else if (!strncmp(line, "tree-set\t", 9)) {
            char *parts[7], *next = line + 9;
            WCHAR rel[FILEES_CFAPI_MAX_PATH], old[FILEES_CFAPI_MAX_IDENTITY], identity[FILEES_CFAPI_MAX_IDENTITY];
            LONGLONG size = 0;
            int i;
            hr = E_INVALIDARG;
            for (i = 0; i < 7 && next; ++i) {
                parts[i] = next; next = strchr(next, '\t');
                if (next) *next++ = 0;
            }
            if (i == 7 && !next && *parts[0] &&
                (!strcmp(parts[2], "f") || !strcmp(parts[2], "d")) &&
                (!strcmp(parts[6], "0") || !strcmp(parts[6], "1")) &&
                release_number(parts[3], &size, 1) &&
                filees_cfapi_widen(parts[1], rel, FILEES_CFAPI_MAX_PATH) &&
                filees_cfapi_widen(parts[4], old, FILEES_CFAPI_MAX_IDENTITY) &&
                filees_cfapi_widen(parts[5], identity, FILEES_CFAPI_MAX_IDENTITY))
                hr = filees_remote_tree(g_root, rel, old, identity, size, *parts[2] == 'd', *parts[6] == '1');
            filees_bridge_say_tree_set(line + 9, hr);
        }
        else if (!strncmp(line, "directory-set\t", 14)) {
            char *parts[6], *cursor = line + 14, **rows = NULL, *end = NULL;
            WCHAR rel[FILEES_CFAPI_MAX_PATH], identity[FILEES_CFAPI_MAX_IDENTITY];
            LONGLONG usn;
            unsigned count, i;
            LONGLONG count64;
            int valid = 1;
            for (i = 0; i < 6; ++i) {
                parts[i] = cursor;
                cursor = strchr(cursor, '\t');
                if (i < 5 && !cursor) { valid = 0; break; }
                if (cursor) *cursor++ = 0;
            }
            /* Bad framing ends the provider: never interpret payload rows as
             * independent fetch replies or commands after a parsing error. */
            if (!valid || cursor) { protocol_error = 1; goto disconnect; }
            if (!release_number(parts[5], &count64, 1) || count64 > FILEES_CFAPI_MAX_ENTRIES) { protocol_error = 1; goto disconnect; }
            count = (unsigned)count64;
            rows = calloc(count ? count : 1, sizeof *rows);
            if (!rows) { protocol_error = 1; goto disconnect; }
            for (i = 0; i < count; ++i) {
                char payload[FILEES_CFAPI_MAX_LINE];
                if (!fgets(payload, sizeof payload, stdin) || !strchr(payload, '\n')) { valid = 0; break; }
                rows[i] = _strdup(payload);
                if (!rows[i]) { valid = 0; break; }
            }
            if (!valid) {
                for (i = 0; i < count; ++i) free(rows[i]);
                free(rows); protocol_error = 1; goto disconnect;
            }
            hr = E_INVALIDARG;
            usn = _strtoi64(parts[2], &end, 10);
            if (*parts[0] && end && end != parts[2] && !*end && usn > 0 &&
                (!strcmp(parts[4], "0") || !strcmp(parts[4], "1")) &&
                filees_cfapi_widen(parts[1], rel, FILEES_CFAPI_MAX_PATH) &&
                filees_cfapi_widen(parts[3], identity, FILEES_CFAPI_MAX_IDENTITY))
                hr = filees_set_directory_state(g_root, rel, identity, usn, *parts[4] == '1', rows, count);
            for (i = 0; i < count; ++i) free(rows[i]);
            free(rows);
            filees_bridge_say_directory_set(parts[0], hr);
        }
        else if (!strncmp(line, "state-set\t", 10)) {
            char *id = line + 10, *rel = strchr(id, '\t'), *token = NULL, *clean = NULL, *end = NULL;
            WCHAR wide[FILEES_CFAPI_MAX_PATH];
            LONGLONG usn = 0;
            hr = E_INVALIDARG;
            if (rel) { *rel++ = '\0'; token = strchr(rel, '\t'); }
            if (token) { *token++ = '\0'; clean = strchr(token, '\t'); }
            if (clean) {
                *clean++ = '\0'; usn = _strtoi64(token, &end, 10);
                if (end && end != token && !*end && usn > 0 &&
                    (!strcmp(clean, "0") || !strcmp(clean, "1")) &&
                    filees_cfapi_widen(rel, wide, FILEES_CFAPI_MAX_PATH))
                    hr = filees_set_local_state(g_root, wide, usn, *clean == '1');
            }
            filees_bridge_say_state_set(id, hr);
        }
        else if (!strncmp(line, "prepare-release-bound\t", 22)) {
            char *id = line + 22, *rel = strchr(id, '\t'), *token = NULL, *source = NULL;
            WCHAR wide[FILEES_CFAPI_MAX_PATH];
            LONGLONG usn = 0;
            hr = E_INVALIDARG;
            if (rel) { *rel++ = '\0'; token = strchr(rel, '\t'); }
            if (token) { *token++ = '\0'; source = strchr(token, '\t'); }
            if (source) {
                *source++ = '\0';
                if (*id && release_number(token, &usn, 0) && !strchr(source, '\t') &&
                    filees_cfapi_widen(rel, wide, FILEES_CFAPI_MAX_PATH))
                    hr = filees_prepare_release_bound(g_root, wide, usn, source);
            }
            filees_bridge_say_release_prepared(id, hr);
        }
        else if (!strncmp(line, "prepare-release\t", 16)) {
            char *id = line + 16, *rel = strchr(id, '\t'), *token = NULL;
            WCHAR wide[FILEES_CFAPI_MAX_PATH];
            LONGLONG usn = 0;
            hr = E_INVALIDARG;
            if (rel) { *rel++ = '\0'; token = strchr(rel, '\t'); }
            if (token) {
                *token++ = '\0';
                if (*id && release_number(token, &usn, 0) &&
                    filees_cfapi_widen(rel, wide, FILEES_CFAPI_MAX_PATH))
                    hr = filees_prepare_release(g_root, wide, usn);
            }
            filees_bridge_say_release_prepared(id, hr);
        }
        else if (!strncmp(line, "restore-release\t", 16)) {
            char *id = line + 16, *rel = strchr(id, '\t'), *token = NULL, *identity = NULL;
            WCHAR wide[FILEES_CFAPI_MAX_PATH], remote_identity[FILEES_CFAPI_MAX_IDENTITY];
            LONGLONG size = -1;
            hr = E_INVALIDARG;
            if (rel) { *rel++ = '\0'; token = strchr(rel, '\t'); }
            if (token) {
                *token++ = '\0';
                identity = strchr(token, '\t');
                if (identity) *identity++ = '\0';
                if (*id && release_number(token, &size, 1) &&
                    filees_cfapi_widen(rel, wide, FILEES_CFAPI_MAX_PATH) &&
                    (!identity || (*identity && !strchr(identity, '\t') && filees_cfapi_widen(identity, remote_identity, FILEES_CFAPI_MAX_IDENTITY))))
                    hr = filees_restore_release(g_root, wide, size, identity ? remote_identity : NULL);
            }
            filees_bridge_say_release_restored(id, hr);
        }
        else if (!strncmp(line, "revert\t", 7)) revert_request(line + 7);
        else filees_bridge_answer(line);
    }

disconnect:
    hr = CfDisconnectSyncRoot(g_connection);
    if (FAILED(hr)) {
        filees_cfapi_fail("disconnect_sync_root", hr);
        return 1;
    }
    return protocol_error;
}
