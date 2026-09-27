package net.filees.mobile

/** Count and weight of a picked tree. Pack trigger is file count (TREE_INGEST). */
object FolderPreflight {
    const val PACK_MIN_FILES = 8

    // Soft batching target. Oversize files stream alone up to the hard limit.
    const val MAX_CHUNK_FILES = 1000 // below the server limit of 5000
    const val MAX_CHUNK_BYTES = 32L * 1024 * 1024

    data class Summary(val files: Int, val bytes: Long) {
        val pack: Boolean get() = files >= PACK_MIN_FILES
    }

    fun of(files: List<WalkedFile>): Summary =
        Summary(files.size, files.sumOf { it.size.coerceAtLeast(0L) })

    // Splits files into consecutive runs bounded by maxBytes. A file already
    // bigger than maxBytes on its own still gets its own one-file chunk,
    // unsplit - this does not, and cannot, save a single file bigger than
    // one session can carry (LARGE_IMPORT_CONCEPT.md's "wieloryb" case: "nie,
    // nigdy" for batching alone). That needs real resumable byte-range
    // transfer, a materially bigger piece of work, not this.
    fun chunkBySize(files: List<WalkedFile>, maxBytes: Long = MAX_CHUNK_BYTES): List<List<WalkedFile>> {
        val chunks = mutableListOf<List<WalkedFile>>()
        var current = mutableListOf<WalkedFile>()
        var currentBytes = 0L
        for (file in files) {
            val size = file.size.coerceAtLeast(0L)
            if (current.isNotEmpty() && (current.size >= MAX_CHUNK_FILES || size == 0L || currentBytes > maxBytes - size)) {
                chunks += current
                current = mutableListOf()
                currentBytes = 0L
            }
            current += file
            currentBytes += size
            if (size == 0L || size >= maxBytes) {
                chunks += current
                current = mutableListOf()
                currentBytes = 0L
            }
        }
        if (current.isNotEmpty()) chunks += current
        return chunks
    }
}
