// Mechanical, fail-closed transformation of an isolated Apache SVN 1.14.5 tree.
// No download, no SQL and no edits to the installed SDK. Hashes are normalized
// for upstream LF vs generated Windows CRLF; every anchor must be unique.
const fs = require('node:fs');
const path = require('node:path');
const crypto = require('node:crypto');
const root = path.resolve(process.argv[2] || '');
if (!process.argv[2] || !root.includes(`${path.sep}tmp${path.sep}`)) throw Error('isolated tmp source tree required');
function read(p) { return fs.readFileSync(path.join(root, p), 'utf8').replace(/\r\n/g, '\n'); }
function replace(s, a, b) {
  if (s.split(a).length !== 2) throw Error(`ambiguous/missing upstream anchor: ${a.slice(0, 70)}`);
  return s.replace(a, b);
}
const files = new Map();
const upstream = {
  'subversion/libsvn_wc/workqueue.c': 'fded75adb12e1f5e709541c7b7d760b146eb3ddc9f0d36f5f185ba3a80e1fa0b',
  'subversion/libsvn_wc/wc_db.c': '9fdb9e705b2958b16866ac07a1b9e9ae13e8edf4ff1f371da356c5cfbccd89d4',
  'subversion/libsvn_wc/wc_db.h': '561cec5f4da317d944b701d0c4b294313517e7d0dcebf50450d50dbeb89d3d1b',
  'subversion/include/private/svn_wc_private.h': '5f3b7aec691d061c0adfc88bd7df68d84b9671a4ee258f64871d53de152827e6',
};
function load(p) {
  const s = read(p);
  if (crypto.createHash('sha256').update(s).digest('hex') !== upstream[p]) throw Error(`unrecognized upstream source: ${p}`);
  files.set(p, s); return s;
}
let db = load('subversion/libsvn_wc/wc_db.c');
db = replace(db, '               svn_skel_t *work_items,\n               apr_pool_t *scratch_pool)\n{\n  svn_sqlite__stmt_t *stmt;',
  '               svn_skel_t *work_items,\n               svn_boolean_t filees_conditional,\n               apr_pool_t *scratch_pool)\n{\n  svn_sqlite__stmt_t *stmt;');
db = replace(db, '  /* Step 1: Create workqueue operations to remove files and dirs in the',
  `  /* FileES private single-file operation supplies the only removal item.
     Reject any WORKING layer rather than silently retaining/copying it. */
  if (filees_conditional && (kind != svn_node_file || wrk_kind != svn_node_file
      || status != svn_wc__db_status_normal || op_depth != 0
      || keep_working || no_delete_wc || !work_items || file_external))
    return svn_error_create(SVN_ERR_WC_OBSTRUCTED_UPDATE, NULL,
                            "FileES conditional exclusion requires a normal BASE file");

  /* Step 1: Create workqueue operations to remove files and dirs in the`);
db = replace(db, '  if (!keep_working && !no_delete_wc)\n', '  if (!keep_working && !no_delete_wc && !filees_conditional)\n');
db = replace(db, '                                     conflict, work_items, scratch_pool),',
  '                                     conflict, work_items, FALSE, scratch_pool),');
db = replace(db, '\nsvn_error_t *\nsvn_wc__db_base_get_info_internal(', `
/* FileES extension: same atomic BASE transition, never a pathname removal. */
svn_error_t *
svn_wc__db_filees_exclude(svn_wc__db_t *db, const char *local_abspath,
                        svn_revnum_t revision, svn_skel_t *work_item,
                        apr_pool_t *scratch_pool)
{
  svn_wc__db_wcroot_t *wcroot;
  const char *local_relpath;
  SVN_ERR_ASSERT(svn_dirent_is_absolute(local_abspath));
  SVN_ERR(svn_wc__db_wcroot_parse_local_abspath(&wcroot, &local_relpath, db,
                              local_abspath, scratch_pool, scratch_pool));
  VERIFY_USABLE_WCROOT(wcroot);
  SVN_WC__DB_WITH_TXN(db_base_remove(wcroot, local_relpath, db, FALSE,
                      FALSE, TRUE, revision, NULL, work_item, TRUE, scratch_pool), wcroot);
  SVN_ERR(flush_entries(wcroot, local_abspath, svn_depth_empty, scratch_pool));
  return SVN_NO_ERROR;
}

svn_error_t *
svn_wc__db_base_get_info_internal(`);
files.set('subversion/libsvn_wc/wc_db.c', db);
let h = load('subversion/libsvn_wc/wc_db.h');
h = replace(h, '\nsvn_error_t *\nsvn_wc__db_base_remove(', `
/* Private FileES guarded file exclusion; work item is created by runtime. */
svn_error_t *
svn_wc__db_filees_exclude(svn_wc__db_t *db, const char *local_abspath,
                        svn_revnum_t revision, svn_skel_t *work_item,
                        apr_pool_t *scratch_pool);

svn_error_t *
svn_wc__db_base_remove(`);
files.set('subversion/libsvn_wc/wc_db.h', h);
let wq = load('subversion/libsvn_wc/workqueue.c');
wq = replace(wq, 'static const struct work_item_dispatch dispatch_table[] = {',
  '#include "filees_release.inc"\n\nstatic const struct work_item_dispatch dispatch_table[] = {\n#ifdef WIN32\n  { "filees-remove-verified-v1", run_filees_remove },\n#endif');
files.set('subversion/libsvn_wc/workqueue.c', wq);
let pub = load('subversion/include/private/svn_wc_private.h');
pub = replace(pub, '#ifdef __cplusplus\n}\n#endif /* __cplusplus */', `
/* FileES private ABI v1. Expected source handle is borrowed, never closed.
   Windows only. No ordinary file-remove fallback. */
svn_error_t *
svn_wc__filees_exclude_retained_v1(svn_wc_context_t *wc_ctx,
                                  const char *local_abspath,
                                  const char *retained_abspath,
                                  void *expected_source_handle,
                                  svn_cancel_func_t cancel_func,
                                  void *cancel_baton,
                                  apr_pool_t *scratch_pool);

#ifdef __cplusplus
}
#endif /* __cplusplus */`);
files.set('subversion/include/private/svn_wc_private.h', pub);
// Modern Expat indents '# define'. Same configure-only fix as the existing SDK.
const generatorPath = 'build/generator/gen_win_dependencies.py';
let generator = read(generatorPath);
for (const version of ['MAJOR', 'MINOR', 'MICRO']) {
  generator = replace(generator, `r'^\\s*#define\\s+XML_${version}_VERSION\\s+(\\d+)'`,
    `r'^\\s*#\\s*define\\s+XML_${version}_VERSION\\s+(\\d+)'`);
}
files.set(generatorPath, generator);
// All source reads/anchors completed before any generated tree is modified.
for (const [p, s] of files) {
  const notice = p.endsWith('.py') ? '# FileES modification: Expat configure compatibility.\n' :
    '/* FileES modification: private conditional file exclusion, 2026-10-06. */\n';
  fs.writeFileSync(path.join(root, p), notice + s);
}
fs.copyFileSync(path.join(__dirname, 'filees_release.inc'), path.join(root, 'subversion/libsvn_wc/filees_release.inc'));
console.log(`Applied FileES conditional exclusion v1 to ${root}`);
