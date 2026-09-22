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

/* One megabyte per CfExecute: large enough that a drawing does not turn into
 * thousands of round trips, small enough to keep progress moving. A transfer
 * length must be a multiple of 4096 unless it ends at the end of the file. */
#define FILEES_CHUNK (1024 * 1024)
/* A fetch the daemon never answers must end. Twenty minutes is far longer
 * than any materialization this is meant for and still finite, because a
 * callback that never answers leaves Explorer hanging for good. */
#define FILEES_FETCH_TIMEOUT_TICKS (20 * 60)

static CF_CONNECTION_KEY g_connection;
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
    if (!filees_bridge_request((const WCHAR *)info->FileIdentity, offset.QuadPart, length.QuadPart,
                               still_waiting, &state, path)) {
        fail_fetch(info, offset, length, STATUS_UNSUCCESSFUL);
        return;
    }
    if (!transfer_file(info, path, offset, length)) {
        fail_fetch(info, offset, length, STATUS_UNSUCCESSFUL);
    }
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

/* Deleting or renaming a placeholder would ask FileES to delete or rename in
 * the repository. That is a decision with consequences for other people, and
 * it belongs in FileES itself, not in a drag inside Explorer. */
static void CALLBACK on_delete(const CF_CALLBACK_INFO *info, const CF_CALLBACK_PARAMETERS *params)
{
    CF_OPERATION_INFO operation;
    CF_OPERATION_PARAMETERS answer;
    HRESULT hr;
    (void)params;
    operation_info(info, CF_OPERATION_TYPE_ACK_DELETE, &operation);
    ZeroMemory(&answer, sizeof answer);
    answer.ParamSize = FILEES_OP_PARAM_SIZE(AckDelete);
    answer.AckDelete.Flags = CF_OPERATION_ACK_DELETE_FLAG_NONE;
    answer.AckDelete.CompletionStatus = STATUS_ACCESS_DENIED;
    hr = CfExecute(&operation, &answer);
    trace("delete refused", hr);
}

static void CALLBACK on_rename(const CF_CALLBACK_INFO *info, const CF_CALLBACK_PARAMETERS *params)
{
    CF_OPERATION_INFO operation;
    CF_OPERATION_PARAMETERS answer;
    HRESULT hr;
    (void)params;
    operation_info(info, CF_OPERATION_TYPE_ACK_RENAME, &operation);
    ZeroMemory(&answer, sizeof answer);
    answer.ParamSize = FILEES_OP_PARAM_SIZE(AckRename);
    answer.AckRename.Flags = CF_OPERATION_ACK_RENAME_FLAG_NONE;
    answer.AckRename.CompletionStatus = STATUS_ACCESS_DENIED;
    hr = CfExecute(&operation, &answer);
    trace("rename refused", hr);
}

static CF_CALLBACK_REGISTRATION k_callbacks[] = {
    {CF_CALLBACK_TYPE_FETCH_DATA, on_fetch_data},
    {CF_CALLBACK_TYPE_CANCEL_FETCH_DATA, on_cancel_fetch_data},
    {CF_CALLBACK_TYPE_NOTIFY_DELETE, on_delete},
    {CF_CALLBACK_TYPE_NOTIFY_RENAME, on_rename},
    CF_CALLBACK_REGISTRATION_END
};

int filees_cfapi_connect(const WCHAR *root)
{
    char line[FILEES_CFAPI_MAX_LINE];
    HRESULT hr;

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
        if (length) filees_bridge_answer(line);
    }

    hr = CfDisconnectSyncRoot(g_connection);
    if (FAILED(hr)) {
        filees_cfapi_fail("disconnect_sync_root", hr);
        return 1;
    }
    return 0;
}
