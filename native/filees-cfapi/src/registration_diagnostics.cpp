// Metadata-only evidence. Never used as an ownership check or repair trigger.
// Opt-in stderr protocol: the daemon persists a bounded private journal.
#include "filees_cfapi.h"
#include <winioctl.h>
#include <sstream>
#include <iomanip>
#include <string>
#include <cstring>
#include <algorithm>
#include <cstddef>

namespace {
std::string hex(const void *data, size_t length) {
    const auto *p = static_cast<const unsigned char *>(data);
    const char *digits = "0123456789abcdef";
    std::string s;
    s.reserve(length*2);
    for (size_t i=0; i<length; ++i) { s += digits[p[i]>>4]; s += digits[p[i]&15]; }
    return s;
}
struct query {
    alignas(CF_SYNC_ROOT_STANDARD_INFO) unsigned char bytes[4096]{};
    DWORD returned = 0;
    HRESULT hr = E_FAIL;
    void read(HANDLE file, CF_SYNC_ROOT_INFO_CLASS kind) {
        hr = CfGetSyncRootInfoByHandle(file, kind, bytes, sizeof bytes, &returned);
    }
    std::string json() const {
        std::ostringstream out;
        out << "{\"hr\":" << static_cast<unsigned long>(hr)
            << ",\"returned\":" << returned << ",\"capacity\":" << sizeof bytes
            << ",\"raw_hex\":\"" << hex(bytes, (std::min)(static_cast<size_t>(returned),sizeof bytes)) << "\"}";
        return out.str();
    }
};
bool terminated(const WCHAR *name, size_t count) { return wmemchr(name,L'\0',count)!=nullptr; }
}

int filees_registration_trace_enabled(void) {
    char value[16]{};
    return GetEnvironmentVariableA("FILEES_REGISTRATION_TRACE",value,sizeof value)>0 &&
        (!strcmp(value,"sample") || !strcmp(value,"anomaly") || !strcmp(value,"mutation"));
}

void filees_registration_trace(const WCHAR *root, const char *operation, const char *phase,
                               HRESULT result, const WCHAR *identity) {
    if (!filees_registration_trace_enabled()) return;
    // All diagnostics are best effort. Neither exceptions nor last-error may
    // alter the operation being observed. No reads of children or file bytes.
    DWORD saved = GetLastError();
    try {
        char correlation[65]{};
        DWORD n = GetEnvironmentVariableA("FILEES_REGISTRATION_OPERATION",correlation,sizeof correlation);
        if (!n || n>=sizeof correlation || strspn(correlation,"0123456789abcdef-")!=n) correlation[0]=0;
        HANDLE file = filees_cfapi_open_path(root,FILE_READ_ATTRIBUTES,
            FILE_SHARE_READ|FILE_SHARE_WRITE|FILE_SHARE_DELETE,nullptr,OPEN_EXISTING,
            FILE_FLAG_BACKUP_SEMANTICS|FILE_FLAG_OPEN_REPARSE_POINT,nullptr);
        DWORD open_error = file==INVALID_HANDLE_VALUE ? GetLastError() : 0;
        query basic, provider, standard;
        BY_HANDLE_FILE_INFORMATION actual{};
        DWORD attributes_error = 0, reparse_error = 0, reparse_bytes = 0, tag = 0;
        if (file!=INVALID_HANDLE_VALUE) {
            if (!GetFileInformationByHandle(file,&actual)) attributes_error=GetLastError();
            basic.read(file,CF_SYNC_ROOT_INFO_BASIC);
            provider.read(file,CF_SYNC_ROOT_INFO_PROVIDER);
            standard.read(file,CF_SYNC_ROOT_INFO_STANDARD);
            unsigned char reparse[MAXIMUM_REPARSE_DATA_BUFFER_SIZE]{};
            if (DeviceIoControl(file,FSCTL_GET_REPARSE_POINT,nullptr,0,reparse,sizeof reparse,&reparse_bytes,nullptr)) {
                if (reparse_bytes>=sizeof tag) memcpy(&tag,reparse,sizeof tag);
            } else reparse_error=GetLastError();
            CloseHandle(file);
        }
        const auto *b = reinterpret_cast<const CF_SYNC_ROOT_BASIC_INFO *>(basic.bytes);
        const auto *p = reinterpret_cast<const CF_SYNC_ROOT_PROVIDER_INFO *>(provider.bytes);
        const auto *s = reinterpret_cast<const CF_SYNC_ROOT_STANDARD_INFO *>(standard.bytes);
        ULONGLONG file_id = (static_cast<ULONGLONG>(actual.nFileIndexHigh)<<32)|actual.nFileIndexLow;
        size_t prefix = offsetof(CF_SYNC_ROOT_STANDARD_INFO,SyncRootIdentity);
        bool complete = !open_error && !attributes_error && SUCCEEDED(basic.hr) &&
            basic.returned>=sizeof *b && SUCCEEDED(provider.hr) && provider.returned>=sizeof *p &&
            SUCCEEDED(standard.hr) && standard.returned>=prefix && standard.returned<=sizeof standard.bytes &&
            s->SyncRootIdentityLength<=standard.returned-prefix;
        bool healthy = complete && file_id==static_cast<ULONGLONG>(b->SyncRootFileId.QuadPart) &&
            file_id==static_cast<ULONGLONG>(s->SyncRootFileId.QuadPart) &&
            terminated(p->ProviderName,ARRAYSIZE(p->ProviderName)) &&
            terminated(s->ProviderName,ARRAYSIZE(s->ProviderName)) &&
            !wcscmp(p->ProviderName,FILEES_CFAPI_PROVIDER) && !wcscmp(s->ProviderName,FILEES_CFAPI_PROVIDER) &&
            s->SyncRootIdentityLength>0;
        // Keep policies in the raw evidence, not in this structural classifier:
        // WinRT normalizes TRACK_ALL to its supported bit mask (0x3ff here).
        if (healthy && identity) healthy=s->SyncRootIdentityLength==(wcslen(identity)+1)*sizeof(WCHAR) &&
            !memcmp(s->SyncRootIdentity,identity,s->SyncRootIdentityLength);
        // A missing/unregistered root is expected before registration or after
        // detach. Only a returned registration with inconsistent data is bad.
        bool anomaly = SUCCEEDED(basic.hr) && basic.returned>=sizeof *b &&
            file_id==static_cast<ULONGLONG>(b->SyncRootFileId.QuadPart) && !healthy;
        CF_PLATFORM_INFO platform{};
        HRESULT platform_hr = CfGetPlatformInfo(&platform);
        typedef LONG (WINAPI *rtl_version)(OSVERSIONINFOW *);
        OSVERSIONINFOW windows{}; windows.dwOSVersionInfoSize=sizeof windows;
        auto rtl = reinterpret_cast<rtl_version>(GetProcAddress(GetModuleHandleW(L"ntdll.dll"),"RtlGetVersion"));
        LONG windows_hr = rtl ? rtl(&windows) : -1;
        FILETIME now{}; GetSystemTimeAsFileTime(&now);
        std::ostringstream out;
        out << "{\"schema\":\"filees.registration-evidence/v1\",\"operation_id\":\"" << correlation
            << "\",\"operation\":\"" << operation << "\",\"phase\":\"" << phase
            << "\",\"pid\":" << GetCurrentProcessId() << ",\"helper_version\":\"" FILEES_CFAPI_VERSION "\""
            << ",\"utc_filetime\":" << ((static_cast<ULONGLONG>(now.dwHighDateTime)<<32)|now.dwLowDateTime)
            << ",\"result\":" << static_cast<unsigned long>(result)
            << ",\"requested_policy\":\"FileES/v1;progressive;always_full;track_all;no_hardlinks\""
            << ",\"root_utf16_hex\":\"" << hex(root,wcslen(root)*sizeof(WCHAR))
            << "\",\"input_identity_hex\":\"" << (identity ? hex(identity,(wcslen(identity)+1)*sizeof(WCHAR)) : "")
            << "\",\"open_error\":" << open_error << ",\"attributes_error\":" << attributes_error
            << ",\"volume\":" << actual.dwVolumeSerialNumber << ",\"file_id\":" << file_id
            << ",\"attributes\":" << actual.dwFileAttributes << ",\"reparse_tag\":" << tag
            << ",\"reparse_returned\":" << reparse_bytes << ",\"reparse_error\":" << reparse_error
            << ",\"healthy\":" << (healthy?"true":"false") << ",\"anomaly\":" << (anomaly?"true":"false")
            << ",\"basic\":" << basic.json() << ",\"provider\":" << provider.json() << ",\"standard\":" << standard.json()
            << ",\"shell\":" << filees_shell_diagnostic(root)
            << ",\"windows_status\":" << windows_hr << ",\"windows_build\":" << windows.dwBuildNumber
            << ",\"platform_hr\":" << static_cast<unsigned long>(platform_hr)
            << ",\"platform_build\":" << platform.BuildNumber << ",\"platform_revision\":" << platform.RevisionNumber
            << ",\"platform_integration\":" << platform.IntegrationNumber << "}";
        auto line = out.str();
        if (line.size()<=32768) { fprintf(stderr,"%s\n",line.c_str()); fflush(stderr); }
    } catch (...) { /* Diagnostics never changes product success/failure. */ }
    SetLastError(saved);
}
