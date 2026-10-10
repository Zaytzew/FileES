/* FileES private metadata-only normalization of a receipted mixed-WC copy.
 * Acquires its own recursive WC lock, AFTER the independently
 * receipted remote move and physical completion. No file IO, ACTUAL changes,
 * work items, commit acknowledgement or repository access. The normal public
 * roots-only acknowledgement follows this atomic layer normalization.
 *
 * move7 represents older children as nested copy operations. They are NOT
 * additional user additions. Validate every copied row against the retained
 * source BASE before replacing those copy layers with one outer copy layer.
 * An intervening scheduled mutation refuses the entire transaction. */
static svn_error_t *
filees_tree_flatten_txn(svn_wc__db_wcroot_t *wcroot, const char *src,
                        const char *dst, svn_revnum_t base, apr_pool_t *pool)
{
  svn_sqlite__stmt_t *stmt;
  svn_boolean_t row, locked;
  int depth = relpath_depth(dst), i;
  const int checks[] = { STMT_FILEES_TREE_PAIR_INVALID, STMT_FILEES_TREE_COPY_INVALID,
                         STMT_FILEES_TREE_COPY_MISSING };
  SVN_ERR(svn_wc__db_wclock_owns_lock_internal(&locked, wcroot, src, FALSE, pool));
  if (!locked)
    return svn_error_create(SVN_ERR_WC_NOT_LOCKED, NULL,
                            "FileES tree normalization requires its owned source lock");
  SVN_ERR(svn_wc__db_wclock_owns_lock_internal(&locked, wcroot, dst, FALSE, pool));
  if (!locked)
    return svn_error_create(SVN_ERR_WC_NOT_LOCKED, NULL,
                            "FileES tree normalization requires its owned destination lock");
  SVN_ERR(svn_sqlite__get_statement(&stmt, wcroot->sdb, STMT_FILEES_WORK_ITEMS));
  SVN_ERR(svn_sqlite__step(&row, stmt));
  SVN_ERR(svn_sqlite__reset(stmt));
  if (row)
    return svn_error_create(SVN_ERR_WC_OBSTRUCTED_UPDATE, NULL,
                            "FileES tree normalization refuses pending work items");
  for (i = 0; i < 3; ++i)
    {
      SVN_ERR(svn_sqlite__get_statement(&stmt, wcroot->sdb, checks[i]));
      SVN_ERR(svn_sqlite__bindf(stmt, "issdr", wcroot->wc_id, src, dst, depth, base));
      if (i == 0) SVN_ERR(svn_sqlite__bind_int(stmt, 6, relpath_depth(src)));
      SVN_ERR(svn_sqlite__step(&row, stmt));
      SVN_ERR(svn_sqlite__reset(stmt));
      if (row)
        return svn_error_create(SVN_ERR_WC_OBSTRUCTED_UPDATE, NULL,
                                "FileES mixed tree copy no longer matches its retained source BASE");
    }
  SVN_ERR(svn_sqlite__get_statement(&stmt, wcroot->sdb, STMT_FILEES_TREE_COPY_REMOVE));
  SVN_ERR(svn_sqlite__bindf(stmt, "isd", wcroot->wc_id, dst, depth));
  SVN_ERR(svn_sqlite__step_done(stmt));
  SVN_ERR(svn_sqlite__get_statement(&stmt, wcroot->sdb, STMT_FILEES_TREE_COPY_INSERT));
  SVN_ERR(svn_sqlite__bindf(stmt, "issds", wcroot->wc_id, src, dst, depth,
                          svn_relpath_dirname(dst, pool)));
  SVN_ERR(svn_sqlite__step_done(stmt));
  return SVN_NO_ERROR;
}

static svn_error_t *
filees_tree_flatten_locked(svn_wc__db_wcroot_t *wcroot, const char *src,
                           const char *dst, svn_revnum_t base, apr_pool_t *pool)
{
  SVN_WC__DB_WITH_TXN(filees_tree_flatten_txn(wcroot, src, dst, base, pool), wcroot);
  return SVN_NO_ERROR;
}

svn_error_t *
svn_wc__filees_tree_flatten_v1(svn_wc_context_t *ctx,
                              const char *source_abspath,
                              const char *destination_abspath,
                              svn_revnum_t base, apr_pool_t *pool)
{
  svn_wc__db_wcroot_t *sw, *dw;
  const char *src, *dst;
  SVN_ERR_ASSERT(svn_dirent_is_absolute(source_abspath)
                 && svn_dirent_is_absolute(destination_abspath));
  SVN_ERR(svn_wc__db_wcroot_parse_local_abspath(&sw, &src, ctx->db,
                    source_abspath, pool, pool));
  SVN_ERR(svn_wc__db_wcroot_parse_local_abspath(&dw, &dst, ctx->db,
                    destination_abspath, pool, pool));
  VERIFY_USABLE_WCROOT(sw);
  VERIFY_USABLE_WCROOT(dw);
  if (sw != dw || !*src || !*dst || !SVN_IS_VALID_REVNUM(base)
      || svn_relpath_skip_ancestor(src, dst) || svn_relpath_skip_ancestor(dst, src))
    return svn_error_create(SVN_ERR_WC_OBSTRUCTED_UPDATE, NULL,
                            "FileES tree normalization refuses a foreign or overlapping scope");
  SVN_WC__CALL_WITH_WRITE_LOCK(filees_tree_flatten_locked(sw, src, dst, base, pool),
                              ctx, sw->abspath, FALSE, pool);
  SVN_ERR(flush_entries(sw, destination_abspath, svn_depth_infinity, pool));
  return SVN_NO_ERROR;
}

/* ACK must use the same DB that owns the compatibility API's write lock.
 * A public info walk sees only the top layer: deleting an unnormalized nested
 * copy can make its row disappear. Check the complete retained source BASE
 * against the outer destination layer, not the visible filesystem/status.
 * Later user add/delete/property layers remain untouched above that layer. */
svn_error_t *
svn_wc__filees_tree_check_v1(svn_wc_adm_access_t *adm,
                            const char *source_abspath,
                            const char *destination_abspath,
                            svn_revnum_t base, apr_pool_t *pool)
{
  svn_wc__db_t *db = svn_wc__adm_get_db(adm);
  svn_wc__db_wcroot_t *sw, *dw;
  const char *src, *dst;
  svn_sqlite__stmt_t *stmt;
  svn_boolean_t row, locked;
  SVN_ERR_ASSERT(svn_dirent_is_absolute(source_abspath)
                 && svn_dirent_is_absolute(destination_abspath));
  SVN_ERR(svn_wc__db_wcroot_parse_local_abspath(&sw, &src, db,
                    source_abspath, pool, pool));
  SVN_ERR(svn_wc__db_wcroot_parse_local_abspath(&dw, &dst, db,
                    destination_abspath, pool, pool));
  VERIFY_USABLE_WCROOT(sw);
  VERIFY_USABLE_WCROOT(dw);
  if (sw != dw || !*src || !*dst || !SVN_IS_VALID_REVNUM(base)
      || svn_relpath_skip_ancestor(src, dst) || svn_relpath_skip_ancestor(dst, src))
    return svn_error_create(SVN_ERR_WC_OBSTRUCTED_UPDATE, NULL,
                            "FileES tree ACK refuses a foreign or overlapping scope");
  SVN_ERR(svn_wc__db_wclock_owns_lock_internal(&locked, sw, src, FALSE, pool));
  if (!locked)
    return svn_error_create(SVN_ERR_WC_NOT_LOCKED, NULL,
                            "FileES tree ACK requires its owned source lock");
  SVN_ERR(svn_wc__db_wclock_owns_lock_internal(&locked, sw, dst, FALSE, pool));
  if (!locked)
    return svn_error_create(SVN_ERR_WC_NOT_LOCKED, NULL,
                            "FileES tree ACK requires its owned destination lock");
  SVN_ERR(svn_sqlite__get_statement(&stmt, sw->sdb, STMT_FILEES_WORK_ITEMS));
  SVN_ERR(svn_sqlite__step(&row, stmt));
  SVN_ERR(svn_sqlite__reset(stmt));
  if (row)
    return svn_error_create(SVN_ERR_WC_OBSTRUCTED_UPDATE, NULL,
                            "FileES tree ACK refuses pending work items");
  SVN_ERR(svn_sqlite__get_statement(&stmt, sw->sdb, STMT_FILEES_TREE_FLAT_MISSING));
  SVN_ERR(svn_sqlite__bindf(stmt, "issdr", sw->wc_id, src, dst,
                           relpath_depth(dst), base));
  SVN_ERR(svn_sqlite__step(&row, stmt));
  SVN_ERR(svn_sqlite__reset(stmt));
  if (row)
    return svn_error_create(SVN_ERR_WC_OBSTRUCTED_UPDATE, NULL,
                            "FileES tree ACK requires a complete normalized copy layer");
  return SVN_NO_ERROR;
}
