package net.filees.mobile

/** How far back the first send of an existing watched folder reaches.
 *  Later files are picked up regardless. Undated files go only with [ALL].
 */
enum class WatchDepth {
    ONLY_NEW,
    DAY,
    WEEK,
    MONTH,
    YEAR,
    ALL,
    ;

    fun includes(file: WalkedFile, now: Long): Boolean = when (this) {
        ALL -> true
        ONLY_NEW -> false
        else -> file.modifiedAt > 0L && file.modifiedAt >= now - windowMs()
    }

    private fun windowMs(): Long = when (this) {
        DAY -> 24L * 60 * 60 * 1000
        WEEK -> 7L * DAY.windowMs()
        MONTH -> 30L * DAY.windowMs()
        YEAR -> 365L * DAY.windowMs()
        ONLY_NEW, ALL -> 0L
    }
}
