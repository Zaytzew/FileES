/* Connecting to one registered anchor and answering the filter.
 *
 * Registration survives a reboot; this connection does not. The daemon starts
 * this verb and keeps it running: the helper holds the connection until its
 * stdin closes, which is how the daemon's own exit takes the anchor offline
 * without a second channel.
 *
 * First cut: the callbacks that must exist, and the two that must say no.
 * Fetching the bytes of a clicked file is the next portion; until it lands,
 * a click is refused with a status that says "not yet", not a crash and not a
 * silent empty file.
 */
#include "filees_cfapi.h"

#include <cfapi.h>
#include <stdio.h>

/* The SDK documents CF_SIZE_OF_OP_PARAM but does not define it in cfapi.h.
 * The filter reads exactly as many bytes as the answered member needs, so the
 * size is the member's offset plus its own size - computed here rather than
 * guessed at with sizeof(the whole union). */
#define FILEES_OP_PARAM_SIZE(field)     (ULONG)(FIELD_OFFSET(CF_OPERATION_PARAMETERS, field) + sizeof(((CF_OPERATION_PARAMETERS *)0)->field))

#ifndef STATUS_NOT_IMPLEMENTED
#define STATUS_NOT_IMPLEMENTED ((NTSTATUS)0xC0000002L)
#endif
#ifndef STATUS_ACCESS_DENIED
#define STATUS_ACCESS_DENIED ((NTSTATUS)0xC0000022L)
#endif

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

/* Every callback runs on a thread pool thread, several at a time, and none of
 * them returns a value: the answer is always a CfExecute. */
static void CALLBACK on_fetch_data(const CF_CALLBACK_INFO *info, const CF_CALLBACK_PARAMETERS *params)
{
    CF_OPERATION_INFO operation;
    CF_OPERATION_PARAMETERS answer;
    (void)params;
    operation_info(info, CF_OPERATION_TYPE_TRANSFER_DATA, &operation);
    ZeroMemory(&answer, sizeof answer);
    answer.ParamSize = FILEES_OP_PARAM_SIZE(TransferData);
    answer.TransferData.Flags = CF_OPERATION_TRANSFER_DATA_FLAG_NONE;
    answer.TransferData.CompletionStatus = STATUS_NOT_IMPLEMENTED;
    answer.TransferData.Buffer = NULL;
    answer.TransferData.Offset = params->FetchData.RequiredFileOffset;
    answer.TransferData.Length = params->FetchData.RequiredLength;
    trace("fetch refused", CfExecute(&operation, &answer));
}

static void CALLBACK on_cancel_fetch_data(const CF_CALLBACK_INFO *info, const CF_CALLBACK_PARAMETERS *params)
{
    /* Nothing is in flight while fetching is unimplemented. The callback is
     * registered anyway, because the pair is what the filter expects and a
     * missing one shows up only under load. */
    (void)info;
    (void)params;
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
    HRESULT hr;
    g_trace = GetEnvironmentVariableW(L"FILEES_CFAPI_TRACE", NULL, 0) > 0;
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

    /* Holding the connection is the whole job from here. Closed stdin, which
     * includes the daemon exiting, ends it. */
    for (;;) {
        int c = getchar();
        if (c == EOF) break;
    }

    hr = CfDisconnectSyncRoot(g_connection);
    if (FAILED(hr)) {
        filees_cfapi_fail("disconnect_sync_root", hr);
        return 1;
    }
    return 0;
}
