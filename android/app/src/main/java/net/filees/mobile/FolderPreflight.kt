package net.filees.mobile

/** Count and weight of a picked tree. Pack trigger is file count (TREE_INGEST). */
object FolderPreflight {
    const val PACK_MIN_FILES = 8

    // sshtransport.Transport.Do sends one whole tree pack as a single
    // uninterrupted SSH session with no chunking or resumption at the wire
    // level (one pre-built frame, one exec, run to completion or fail
    // outright). implementation notes (not distributed) measured a hard ~11-minute
    // session ceiling on one particular relayd-tunneled route regardless of
    // keepalives (~10.6 MB/s there, so ~6 GB) - the phone runs on cellular,
    // which is routinely far slower and far less stable, and the ceiling on
    // any other route is simply unknown (that concept's own §4.3: "skąd
    // klient ma znać limit sesji: znikąd"). Rather than learning any one
    // route's real number, chunkBySize keeps every single pack small enough
    // to fit comfortably even under a bad, slow link, so a big batch is many
    // small, independent, individually-resumable sessions instead of one
    // long one that a real-world network is never guaranteed to hold open.
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
            if (current.isNotEmpty() && currentBytes + size > maxBytes) {
                chunks += current
                current = mutableListOf()
                currentBytes = 0L
            }
            current += file
            currentBytes += size
        }
        if (current.isNotEmpty()) chunks += current
        return chunks
    }
}
