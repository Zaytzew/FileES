package net.filees.mobile

/** Count and weight of a picked tree. Pack trigger is file count (TREE_INGEST). */
object FolderPreflight {
    const val PACK_MIN_FILES = 8

    data class Summary(val files: Int, val bytes: Long) {
        val pack: Boolean get() = files >= PACK_MIN_FILES
    }

    fun of(files: List<WalkedFile>): Summary =
        Summary(files.size, files.sumOf { it.size.coerceAtLeast(0L) })
}
