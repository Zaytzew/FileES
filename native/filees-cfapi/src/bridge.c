/* Asking the daemon for a path, over this process's own stdin and stdout.
 *
 * The helper could speak the daemon's IPC itself, but that would put JSON, a
 * socket and a contract into C for no gain. The split stays where it was:
 * the daemon knows Subversion, the helper knows the shell, and the call
 * between them is one line each way.
 *
 *   helper -> daemon   fetch    <TAB> id <TAB> offset <TAB> length <TAB> identity <TAB> process
 *   daemon -> helper   ok       <TAB> id <TAB> absolute path
 *                      err      <TAB> id <TAB> reason
 *   helper -> daemon   hydrated <TAB> id <TAB> identity      (bytes handed over)
 *   daemon -> helper   revert   <TAB> id <TAB> absolute path (connect.c)
 *   helper -> daemon   reverted <TAB> id <TAB> HRESULT
 *   daemon -> helper   state-set <TAB> id <TAB> relative path <TAB> USN <TAB> 0|1
 *   helper -> daemon   state-set <TAB> id <TAB> HRESULT
 *
 * process is the image path of the program opening the file, so the daemon
 * can refuse a program that tries to download a whole anchor. hydrated tells
 * the daemon the file is complete on disk: only then may Subversion read it,
 * because reading a placeholder that is still partial would ask for it again.
 *
 * Callbacks arrive on several thread pool threads at once, so requests carry
 * an id and one reader thread hands each answer to the thread waiting for it.
 * Answers may come back in any order; the daemon is free to serialize the
 * work behind them, which it does anyway for a working copy.
 */
#include "filees_cfapi.h"

#include <stdio.h>
#include <string.h>
#include <limits.h>
#include <stdlib.h>
#include <errno.h>

#define FILEES_BRIDGE_SLOTS 64

struct slot {
    int used;
    int id;
    int ok;
    int kind; /* 1 = fetch; 2 = isolated typed delete, never interchangeable */
    int answered;
    HANDLE done;
    WCHAR path[FILEES_CFAPI_MAX_PATH];
};

static CRITICAL_SECTION g_lock;
static struct slot g_slots[FILEES_BRIDGE_SLOTS];
static int g_next_id = 1;
static int g_started;

void filees_bridge_start(void)
{
    InitializeCriticalSection(&g_lock);
    g_started = 1;
}

static struct slot *take_slot(int *id, int kind)
{
    int i;
    EnterCriticalSection(&g_lock);
    /* Never wrap and accidentally deliver a late reply to a reused token. */
    if (g_next_id == INT_MAX) { LeaveCriticalSection(&g_lock); return NULL; }
    for (i = 0; i < FILEES_BRIDGE_SLOTS; ++i) {
        if (!g_slots[i].used) {
            g_slots[i].used = 1;
            g_slots[i].ok = 0;
            g_slots[i].kind = kind;
            g_slots[i].answered = 0;
            g_slots[i].id = g_next_id++;
            g_slots[i].path[0] = L'\0';
            *id = g_slots[i].id;
            LeaveCriticalSection(&g_lock);
            return &g_slots[i];
        }
    }
    LeaveCriticalSection(&g_lock);
    return NULL;
}

static void release_slot(struct slot *slot)
{
    EnterCriticalSection(&g_lock);
    slot->used = 0;
    LeaveCriticalSection(&g_lock);
}

/* One line out, under the lock: two callbacks writing at once would produce a
 * line the daemon cannot read, and it would look like a protocol bug on the
 * far side. */
static int send_request(int id, LONGLONG offset, LONGLONG length, const WCHAR *identity, const WCHAR *process)
{
    char utf8[FILEES_CFAPI_MAX_IDENTITY * 4];
    char image[FILEES_CFAPI_MAX_PATH * 4];
    int written;
    if (!WideCharToMultiByte(CP_UTF8, 0, identity, -1, utf8, (int)sizeof utf8, NULL, NULL)) return 0;
    if (strchr(utf8, '\t') || strchr(utf8, '\n')) return 0;
    /* An unreadable image path is not a reason to refuse the file; the
     * daemon then only knows that it does not know who asked. */
    if (!process || !WideCharToMultiByte(CP_UTF8, 0, process, -1, image, (int)sizeof image, NULL, NULL) ||
        strchr(image, '\t') || strchr(image, '\n'))
        strcpy_s(image, sizeof image, "?");
    EnterCriticalSection(&g_lock);
    written = printf("fetch\t%d\t%lld\t%lld\t%s\t%s\n", id, offset, length, utf8, image);
    fflush(stdout);
    LeaveCriticalSection(&g_lock);
    return written > 0;
}

/* filees_bridge_answer is called by the reader thread for every line the
 * daemon sends. An answer for a request nobody waits for any more (a cancelled
 * fetch, a timed out one) is dropped rather than treated as an error. */
void filees_bridge_answer(char *line)
{
    char *kind = line, *cursor, *rest;
    char *end_id;
    long parsed_id;
    int id, i;
    cursor = strchr(line, '\t');
    if (!cursor) return;
    *cursor++ = '\0';
    rest = strchr(cursor, '\t');
    if (!rest) return;
    *rest++ = '\0';
    errno = 0;
    parsed_id = strtol(cursor, &end_id, 10);
    if (errno || *cursor < '1' || *cursor > '9' || *end_id || parsed_id <= 0 || parsed_id >= INT_MAX) return;
    id = (int)parsed_id;
    EnterCriticalSection(&g_lock);
    for (i = 0; i < FILEES_BRIDGE_SLOTS; ++i) {
        if (!g_slots[i].used || g_slots[i].id != id) continue;
#ifdef FILEES_CFAPI_DELETE_PROBE
        if (g_slots[i].kind == 2) {
            /* Typed, canonical, exact token. A fetch 'ok' cannot authorize
             * deletion even if it happens to carry the same numeric id. */
            if (strcmp(kind, "delete-allow") && strcmp(kind, "delete-deny")) break;
            if (strcmp(rest, "1") || g_slots[i].answered) break;
            g_slots[i].answered = 1;
            g_slots[i].ok = !strcmp(kind, "delete-allow");
            SetEvent(g_slots[i].done);
            break;
        }
#endif
        if (strcmp(kind, "ok") && strcmp(kind, "err")) break;
        g_slots[i].ok = !strcmp(kind, "ok") &&
                        MultiByteToWideChar(CP_UTF8, 0, rest, -1, g_slots[i].path, FILEES_CFAPI_MAX_PATH) > 0;
        SetEvent(g_slots[i].done);
        break;
    }
    LeaveCriticalSection(&g_lock);
}

int filees_bridge_request(const WCHAR *identity, const WCHAR *process, LONGLONG offset, LONGLONG length,
                          filees_bridge_waiting waiting, void *context, WCHAR *path, int *id_out)
{
    struct slot *slot;
    int id = 0, ok = 0;
    if (!g_started) return 0;
    slot = take_slot(&id, 1);
    if (!slot) return 0;
    if (!slot->done) slot->done = CreateEventW(NULL, FALSE, FALSE, NULL);
    if (!slot->done) {
        release_slot(slot);
        return 0;
    }
    ResetEvent(slot->done);
    if (id_out) *id_out = id;
    if (!send_request(id, offset, length, identity, process)) {
        release_slot(slot);
        return 0;
    }
    /* Waiting in one second steps rather than one long wait: the caller uses
     * the gaps to tell Windows the download is still going, which is what
     * keeps Explorer and the application from giving up on the handle. */
    for (;;) {
        DWORD waited = WaitForSingleObject(slot->done, 1000);
        if (waited == WAIT_OBJECT_0) {
            ok = slot->ok;
            if (ok) wcscpy_s(path, FILEES_CFAPI_MAX_PATH, slot->path);
            break;
        }
        if (waited != WAIT_TIMEOUT) break;
        if (waiting && !waiting(context)) break;
    }
    release_slot(slot);
    return ok;
}

void filees_bridge_say_hydrated(int id, const WCHAR *identity)
{
    char utf8[FILEES_CFAPI_MAX_IDENTITY * 4];
    if (!WideCharToMultiByte(CP_UTF8, 0, identity, -1, utf8, (int)sizeof utf8, NULL, NULL)) return;
    EnterCriticalSection(&g_lock);
    printf("hydrated\t%d\t%s\n", id, utf8);
    fflush(stdout);
    LeaveCriticalSection(&g_lock);
}

/* The answer to a revert shares stdout with fetch requests, so it goes out
 * under the same lock: one line, never interleaved with another. */
void filees_bridge_say_reverted(const char *id, HRESULT hr)
{
    EnterCriticalSection(&g_lock);
    printf("reverted\t%s\t0x%08lx\n", id, (unsigned long)hr);
    fflush(stdout);
    LeaveCriticalSection(&g_lock);
}

void filees_bridge_say_state_set(const char *id, HRESULT hr)
{
    EnterCriticalSection(&g_lock);
    printf("state-set\t%s\t0x%08lx\n", id, (unsigned long)hr);
    fflush(stdout);
    LeaveCriticalSection(&g_lock);
}

void filees_bridge_say_directory_set(const char *id, HRESULT hr)
{
    EnterCriticalSection(&g_lock);
    printf("directory-set\t%s\t0x%08lx\n", id, (unsigned long)hr);
    fflush(stdout);
    LeaveCriticalSection(&g_lock);
}

void filees_bridge_say_release_prepared(const char *id, HRESULT hr)
{
    EnterCriticalSection(&g_lock);
    printf("release-prepared\t%s\t0x%08lx\n", id, (unsigned long)hr);
    fflush(stdout);
    LeaveCriticalSection(&g_lock);
}

void filees_bridge_say_release_restored(const char *id, HRESULT hr)
{
    EnterCriticalSection(&g_lock);
    printf("release-restored\t%s\t0x%08lx\n", id, (unsigned long)hr);
    fflush(stdout);
    LeaveCriticalSection(&g_lock);
}

void filees_bridge_say_tree_set(const char *id, HRESULT hr)
{
    EnterCriticalSection(&g_lock);
    printf("tree-set\t%s\t0x%08lx\n", id, (unsigned long)hr);
    fflush(stdout);
    LeaveCriticalSection(&g_lock);
}

void filees_bridge_say_tree_move(const char *id, HRESULT hr)
{
    EnterCriticalSection(&g_lock);
    printf("tree-moved\t%s\t0x%08lx\n", id, (unsigned long)hr);
    fflush(stdout);
    LeaveCriticalSection(&g_lock);
}

void filees_bridge_say_tree_identity(const char *id, HRESULT hr)
{
    EnterCriticalSection(&g_lock);
    printf("tree-identity\t%s\t0x%08lx\n", id, (unsigned long)hr);
    fflush(stdout);
    LeaveCriticalSection(&g_lock);
}

#ifdef FILEES_CFAPI_DELETE_PROBE
static int delete_text(const void *identity, DWORD identity_length, const WCHAR *path,
    char *utf8, size_t utf8_size, char *full_path, size_t path_size)
{
    WCHAR wide[FILEES_CFAPI_MAX_IDENTITY];
    size_t length;
    if (!identity || identity_length < sizeof(WCHAR) || identity_length > sizeof wide ||
        identity_length % sizeof(WCHAR) || !path) return 0;
    length = identity_length / sizeof(WCHAR);
    memcpy(wide, identity, identity_length);
    if (wide[length - 1] != L'\0' || wcsnlen(wide, length) != length - 1) return 0;
    return WideCharToMultiByte(CP_UTF8, WC_ERR_INVALID_CHARS, wide, -1, utf8, (int)utf8_size, NULL, NULL) &&
        WideCharToMultiByte(CP_UTF8, WC_ERR_INVALID_CHARS, path, -1, full_path, (int)path_size, NULL, NULL) &&
        !strpbrk(utf8, "\t\r\n") && !strpbrk(full_path, "\t\r\n");
}

int filees_bridge_say_delete_probe(const char *phase, DWORD volume,
    LONGLONG root_id, LONGLONG file_id, LONGLONG request, DWORD flags,
    const void *identity, DWORD identity_length, const WCHAR *path, DWORD process)
{
    char utf8[FILEES_CFAPI_MAX_IDENTITY * 4], full_path[FILEES_CFAPI_MAX_PATH * 4];
    int written;
    /* Blobs are length-delimited. Do not read beyond them or ignore suffixes. */
    if (!delete_text(identity, identity_length, path, utf8, sizeof utf8, full_path, sizeof full_path)) return 0;
    EnterCriticalSection(&g_lock);
    written = printf("delete-probe\t%s\t%lu\t%lld\t%lld\t%lld\t%lu\t%s\t%s\t%lu\n",
        phase, (unsigned long)volume, root_id, file_id, request, (unsigned long)flags,
        utf8, full_path, (unsigned long)process);
    if (fflush(stdout)) written = -1;
    LeaveCriticalSection(&g_lock);
    return written > 0;
}

static int send_delete_frame(const char *kind, int id, DWORD volume,
    LONGLONG root_id, LONGLONG file_id, LONGLONG request, DWORD flags,
    const void *identity, DWORD identity_length, const WCHAR *path, DWORD process)
{
    char utf8[FILEES_CFAPI_MAX_IDENTITY * 4], full_path[FILEES_CFAPI_MAX_PATH * 4];
    int written;
    if (!delete_text(identity, identity_length, path, utf8, sizeof utf8, full_path, sizeof full_path)) return 0;
    EnterCriticalSection(&g_lock);
    written = printf("%s\t%d\t%lu\t%llu\t%llu\t%lld\t%lu\t%s\t%s\t%lu\n",
        kind, id, (unsigned long)volume, (ULONGLONG)root_id, (ULONGLONG)file_id, request,
        (unsigned long)flags, utf8, full_path, (unsigned long)process);
    if (fflush(stdout)) written = -1;
    LeaveCriticalSection(&g_lock);
    return written > 0;
}

int filees_bridge_delete_request(DWORD volume, LONGLONG root_id, LONGLONG file_id,
    LONGLONG request, DWORD flags, const void *identity, DWORD identity_length,
    const WCHAR *path, DWORD process, int *id_out)
{
    struct slot *slot;
    int id, ok = 0;
    if (!g_started || flags || !process || !request) return 0;
    slot = take_slot(&id, 2);
    if (!slot) return 0;
    if (!slot->done) slot->done = CreateEventW(NULL, FALSE, FALSE, NULL);
    if (!slot->done) { release_slot(slot); return 0; }
    ResetEvent(slot->done);
    *id_out = id;
    if (send_delete_frame("delete-request", id, volume, root_id, file_id, request,
        flags, identity, identity_length, path, process) &&
        WaitForSingleObject(slot->done, 10000) == WAIT_OBJECT_0) ok = slot->ok;
    release_slot(slot);
    return ok;
}

void filees_bridge_say_delete_acked(int id, HRESULT hr)
{
    EnterCriticalSection(&g_lock);
    printf("delete-acked\t%d\t0x%08lx\n", id, (unsigned long)hr);
    fflush(stdout);
    LeaveCriticalSection(&g_lock);
}

void filees_bridge_say_delete_complete(DWORD volume, LONGLONG root_id, LONGLONG file_id,
    LONGLONG request, DWORD flags, const void *identity, DWORD identity_length,
    const WCHAR *path, DWORD process)
{
    send_delete_frame("delete-complete", 0, volume, root_id, file_id, request,
        flags, identity, identity_length, path, process);
}
#endif
