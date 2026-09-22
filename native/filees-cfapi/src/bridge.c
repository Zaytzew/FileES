/* Asking the daemon for a path, over this process's own stdin and stdout.
 *
 * The helper could speak the daemon's IPC itself, but that would put JSON, a
 * socket and a contract into C for no gain. The split stays where it was:
 * the daemon knows Subversion, the helper knows the shell, and the call
 * between them is one line each way.
 *
 *   helper -> daemon   fetch <TAB> id <TAB> offset <TAB> length <TAB> identity
 *   daemon -> helper   ok    <TAB> id <TAB> absolute path
 *                      err   <TAB> id <TAB> reason
 *
 * Callbacks arrive on several thread pool threads at once, so requests carry
 * an id and one reader thread hands each answer to the thread waiting for it.
 * Answers may come back in any order; the daemon is free to serialize the
 * work behind them, which it does anyway for a working copy.
 */
#include "filees_cfapi.h"

#include <stdio.h>
#include <string.h>

#define FILEES_BRIDGE_SLOTS 64

struct slot {
    int used;
    int id;
    int ok;
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

static struct slot *take_slot(int *id)
{
    int i;
    EnterCriticalSection(&g_lock);
    for (i = 0; i < FILEES_BRIDGE_SLOTS; ++i) {
        if (!g_slots[i].used) {
            g_slots[i].used = 1;
            g_slots[i].ok = 0;
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
static int send_request(int id, LONGLONG offset, LONGLONG length, const WCHAR *identity)
{
    char utf8[FILEES_CFAPI_MAX_IDENTITY * 4];
    int written;
    if (!WideCharToMultiByte(CP_UTF8, 0, identity, -1, utf8, (int)sizeof utf8, NULL, NULL)) return 0;
    if (strchr(utf8, '\t') || strchr(utf8, '\n')) return 0;
    EnterCriticalSection(&g_lock);
    written = printf("fetch\t%d\t%lld\t%lld\t%s\n", id, offset, length, utf8);
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
    int id, i;
    cursor = strchr(line, '\t');
    if (!cursor) return;
    *cursor++ = '\0';
    rest = strchr(cursor, '\t');
    if (!rest) return;
    *rest++ = '\0';
    id = atoi(cursor);
    EnterCriticalSection(&g_lock);
    for (i = 0; i < FILEES_BRIDGE_SLOTS; ++i) {
        if (!g_slots[i].used || g_slots[i].id != id) continue;
        g_slots[i].ok = !strcmp(kind, "ok") &&
                        MultiByteToWideChar(CP_UTF8, 0, rest, -1, g_slots[i].path, FILEES_CFAPI_MAX_PATH) > 0;
        SetEvent(g_slots[i].done);
        break;
    }
    LeaveCriticalSection(&g_lock);
}

int filees_bridge_request(const WCHAR *identity, LONGLONG offset, LONGLONG length,
                          filees_bridge_waiting waiting, void *context, WCHAR *path)
{
    struct slot *slot;
    int id = 0, ok = 0;
    if (!g_started) return 0;
    slot = take_slot(&id);
    if (!slot) return 0;
    if (!slot->done) slot->done = CreateEventW(NULL, FALSE, FALSE, NULL);
    if (!slot->done) {
        release_slot(slot);
        return 0;
    }
    ResetEvent(slot->done);
    if (!send_request(id, offset, length, identity)) {
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
