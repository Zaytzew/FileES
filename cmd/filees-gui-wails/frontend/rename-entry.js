// The folder window offers the display name from two places: the row in the
// action list and the button beside the heading. They answer one question, so
// they share one rule - an entry shown in one place and missing from the other
// would read as two features with two different conditions.
//
// Not in the detail views (shares, grants, uploads, quarantine, shelf), which
// take the action list's place, and not while the form is already open.
export function renameOffered(detailMode, renaming) {
  return !detailMode && !renaming;
}
