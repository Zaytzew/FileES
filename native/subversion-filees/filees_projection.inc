/* Private, process-local opt-in. The callback is invoked only by the actual
 * file-remove work item under SVN's writer. It never grants a remote delete.
 * A handled result MUST NOT fall back to pathname unlink: a different object
 * might already occupy that name after the provider removed the pinned one. */
#ifdef WIN32
typedef svn_error_t *(*filees_projection_fn)(void *, const char *, int,
                                            svn_boolean_t *, apr_pool_t *);
static __declspec(thread) filees_projection_fn filees_projection_callback;
static __declspec(thread) void *filees_projection_baton;

void svn_wc__filees_projection_v1(filees_projection_fn callback, void *baton)
{
  filees_projection_callback = callback;
  filees_projection_baton = baton;
}
#endif
