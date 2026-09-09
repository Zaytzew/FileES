#include "filees_svn.h"

#include <string.h>
#include <apr_strings.h>
#include <svn_dirent_uri.h>

#define WRITER_HEADER "filees.native-writer/v1\n"

/* A stable inode/handle, never unlink or replace it. The OS releases the lock
 * on process death. The bounded record survives and fences subsequent native
 * mutations until the SAME remote receipt can be reconciled. This coordinates
 * FileES helpers, not arbitrary SVN programs which bypass this protocol. */
svn_error_t *filees_writer_open(apr_file_t **file, const char **pending,
                                 const char *wc, apr_pool_t *pool)
{
    const char *path = svn_dirent_join(wc, ".svn/filees-native-writer-v1", pool);
    apr_finfo_t info;
    apr_status_t status;
    char record[512];
    apr_size_t n;
    *pending = NULL;
    status = apr_stat(&info, path, APR_FINFO_TYPE | APR_FINFO_LINK | APR_FINFO_NLINK, pool);
    if (status == APR_SUCCESS) {
        SVN_ERR(filees_plain_node(path, APR_REG, FALSE, pool));
        if ((info.valid & APR_FINFO_NLINK) && info.nlink != 1)
            return filees_refuse("native writer record must not be hardlinked");
    } else if (!APR_STATUS_IS_ENOENT(status)) {
        return svn_error_wrap_apr(status, "cannot inspect native writer record");
    }
    SVN_ERR(filees_plain_node(svn_dirent_join(wc, ".svn", pool), APR_DIR, FALSE, pool));
    status = apr_file_open(file, path, APR_FOPEN_READ | APR_FOPEN_WRITE | APR_FOPEN_CREATE | APR_FOPEN_BINARY,
                          APR_FPROT_UREAD | APR_FPROT_UWRITE, pool);
    if (status) return svn_error_wrap_apr(status, "cannot open native writer record");
    status = apr_file_inherit_unset(*file);
    if (status) return svn_error_wrap_apr(status, "cannot isolate native writer handle");
    status = apr_file_lock(*file, APR_FLOCK_EXCLUSIVE | APR_FLOCK_NONBLOCK);
    if (status) return svn_error_create(SVN_ERR_WC_LOCKED, NULL, "another FileES native writer is active");
    status = apr_file_info_get(&info, APR_FINFO_SIZE, *file);
    if (status) return svn_error_wrap_apr(status, "cannot size native writer record");
    if (!info.size) return SVN_NO_ERROR;
    if (info.size >= (apr_off_t)sizeof(record) || info.size <= (apr_off_t)strlen(WRITER_HEADER) + 1)
        return filees_refuse("invalid native writer record; retained for inspection");
    n = (apr_size_t)info.size;
    status = apr_file_read_full(*file, record, n, NULL);
    if (status) return svn_error_wrap_apr(status, "cannot read native writer record");
    if (memcmp(record, WRITER_HEADER, strlen(WRITER_HEADER)) || record[n-1] != '\n' || memchr(record, 0, n))
        return filees_refuse("invalid native writer record; retained for inspection");
    record[n-1] = 0;
    *pending = apr_pstrdup(pool, record + strlen(WRITER_HEADER));
    if (strchr(*pending, '\n') || strchr(*pending, '\r'))
        return filees_refuse("invalid native writer identity");
    return SVN_NO_ERROR;
}

svn_error_t *filees_writer_set(apr_file_t *file, const char *marker, apr_pool_t *pool)
{
    apr_status_t status;
    apr_off_t offset = 0;
    if (marker && (!*marker || strlen(marker) > 128 || strchr(marker, '\n') || strchr(marker, '\r')))
        return filees_refuse("invalid native writer identity");
    status = apr_file_trunc(file, 0);
    if (!status) status = apr_file_seek(file, APR_SET, &offset);
    if (!status && marker) {
        const char *record = apr_pstrcat(pool, WRITER_HEADER, marker, "\n", NULL);
        status = apr_file_write_full(file, record, strlen(record), NULL);
    }
    if (!status) status = apr_file_sync(file);
    if (status) return svn_error_wrap_apr(status, "cannot persist native writer identity");
    return SVN_NO_ERROR;
}

svn_error_t *filees_writer_guard(const char *wc, apr_pool_t *pool)
{
    apr_file_t *file;
    const char *pending;
    SVN_ERR(filees_writer_open(&file, &pending, wc, pool));
    if (pending) return filees_refuse("unfinished native commit requires receipt recovery before mutation");
    return SVN_NO_ERROR;
}
