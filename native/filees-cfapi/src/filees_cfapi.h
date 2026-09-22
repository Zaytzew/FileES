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

#define FILEES_CFAPI_SCHEMA "filees.cfapi/v1"
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

/* UTF-8 argv into wide strings; the shell is wide everywhere. */
int filees_cfapi_widen(const char *utf8, WCHAR *out, size_t out_chars);

/* The call to the daemon (bridge.c). waiting() is invoked about once a second
 * while an answer has not arrived; returning 0 abandons the request. */
typedef int (*filees_bridge_waiting)(void *context);

void filees_bridge_start(void);
void filees_bridge_answer(char *line);
int filees_bridge_request(const WCHAR *identity, LONGLONG offset, LONGLONG length,
                          filees_bridge_waiting waiting, void *context, WCHAR *path);

/* Verbs. Each returns a process exit code and has already printed its JSON. */
int filees_cfapi_register(const WCHAR *root, const WCHAR *identity);
int filees_cfapi_unregister(const WCHAR *root);
int filees_cfapi_info(const WCHAR *root);
int filees_cfapi_placeholders(const WCHAR *root, const WCHAR *relative);
int filees_cfapi_connect(const WCHAR *root);

#endif
