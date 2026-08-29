package net.filees.mobile

object FileesSession {
    const val PREFS = "filees_connection"
    const val PREF_ADDRESS = "address"
    const val PREF_HOST_KEY = "host_public_key"
    const val PREF_REPO_ID = "selected_repo_id"
    // Deliberately separate from PREF_REPO_ID, which tracks "which repo is
    // currently open in the browser" and is CLEARED on navigating back to
    // the top-level list (MainActivity#goUp) - the watched-folder upload
    // target must survive that, or every tick after leaving a repository
    // silently no-ops (found live: added a watched folder, took a photo,
    // nothing happened, no explanation).
    const val PREF_UPLOAD_REPO_ID = "watch_upload_repo_id"
    // Stored alongside PREF_UPLOAD_REPO_ID purely for display (Settings
    // label, upload notification text) - avoids an extra network round trip
    // to re-resolve a display name from the repo list on every tick.
    const val PREF_UPLOAD_REPO_NAME = "watch_upload_repo_name"
    const val PREF_DETAILS = "last_details"
    const val MOBILE_USER = "_filees-mobile"
}
