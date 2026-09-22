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
