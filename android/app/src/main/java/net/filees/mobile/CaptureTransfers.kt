package net.filees.mobile

import android.content.Context
import android.os.CancellationSignal
import androidbind.Client
import org.json.JSONArray
import java.io.Closeable
import java.io.File
import java.io.IOException
import java.io.InputStream
import java.io.InterruptedIOException
import java.io.OutputStream
import java.util.concurrent.TimeUnit
import java.util.concurrent.locks.ReentrantLock

/** One owner of scan/spool/drain across foreground and WorkManager. */
object CaptureCoordinator {
    private val lock = ReentrantLock()
    fun <T> run(cancel: CaptureCancellation, action: () -> T): T {
        while (!lock.tryLock(100, TimeUnit.MILLISECONDS)) cancel.check()
        try { cancel.check(); return action() } finally { lock.unlock() }
    }
}

class CaptureCancellation {
    val signal = CancellationSignal()
    @Volatile private var stopped = false
    @Volatile var stopReason: Int = androidx.work.WorkInfo.STOP_REASON_UNKNOWN
        private set
    val isCancelled: Boolean get() = stopped || Thread.currentThread().isInterrupted
    private var client: Client? = null
    private val streams = mutableSetOf<Closeable>()
    @Synchronized fun attach(client: Client) { check(); this.client = client }
    @Synchronized fun cancel(reason: Int = androidx.work.WorkInfo.STOP_REASON_UNKNOWN) {
        stopReason = reason
        stopped = true
        signal.cancel()
        client?.cancel()
        streams.toList().forEach { runCatching { it.close() } }
    }
    fun check() { if (isCancelled) throw InterruptedIOException("Capture cancelled") }
    fun <T> reading(stream: InputStream, action: (InputStream) -> T): T {
        try { synchronized(this) { check(); streams.add(stream) } }
        catch (e: Exception) { stream.close(); throw e }
        return stream.use { try { action(it) } finally { synchronized(this) { streams.remove(stream) } } }
    }
}

/** Shared capture implementation; UI receives real outcomes of concrete IDs. */
object CaptureTransfers {
    const val MAX_FILE_BYTES = 2L * 1024 * 1024 * 1024
    data class Result(var sent: Int = 0, var waiting: Int = 0, val errors: MutableList<String> = mutableListOf())
    fun source(file: WalkedFile) = file.uri.toString() + "/" + file.filename

    fun copy(input: InputStream, output: OutputStream, limit: Long, cancel: CaptureCancellation): Long {
        var total = 0L
        val buffer = ByteArray(64 * 1024)
        while (true) {
            cancel.check()
            val n = input.read(buffer)
            if (n < 0) break
            total += n
            if (total > limit) throw IOException("Capture exceeds size limit ($limit bytes)")
            output.write(buffer, 0, n)
        }
        return total
    }

    fun send(context: Context, client: Client, repoId: String, files: List<WalkedFile>, packed: Boolean,
             cancel: CaptureCancellation, watched: WatchedFolders? = null, queueOnly: Boolean = false, progress: (() -> Unit)? = null): Result = CaptureCoordinator.run(cancel) {
        cancel.attach(client)
        val result = Result()
        val toSend = linkedMapOf<String, Int>()
        val existing = PendingUpload.listFromJson(client.listUploadsJSON(repoId))
        // Watch must not enqueue a second intent for a source already durably
        // queued, including a conflict. Completed metadata closes a crash
        // between receipt persistence and the SharedPreferences seen marker.
        val owned = existing.filter { watched != null || !it.delivered }.flatMap { it.sources }.toSet()
        existing.filter { it.delivered }.forEach { item -> item.sources.forEach { watched?.markSeen(it) } }
        val fresh = files.filterNot { source(it) in owned }

        fun account(item: PendingUpload, count: Int) {
            if (item.delivered) {
                result.sent += count
                item.sources.forEach { watched?.markSeen(it) }
            } else {
                result.waiting += count
                if (item.lastError.isNotBlank()) result.errors += item.lastError
            }
        }
        // An explicit retry of a selection reuses each pending ID once.
        if (watched == null) {
            val selected = files.map { source(it) }.toSet()
            existing.filter { !it.delivered && it.sources.any(selected::contains) }.forEach {
                toSend[it.id] = it.fileCount
            }
        }

        fun sendChunk(chunk: List<WalkedFile>, tree: Boolean) {
            cancel.check()
            progress?.invoke()
            var spool: File? = null
            var queued = false
            try {
                val sources = JSONArray(chunk.map(::source)).toString()
                val id: String
                if (tree) {
                    spool = TreeZip.pack(context.contentResolver, chunk, context.cacheDir, cancel)
                    id = client.enqueueTreeFile(repoId, UploadPaths.ROOT, chunk.size.toLong(), spool.absolutePath, sources)
                } else {
                    val file = chunk.single()
                    spool = File.createTempFile("capture-", ".tmp", context.cacheDir)
                    val input = context.contentResolver.openAssetFileDescriptor(file.uri, "r", cancel.signal)
                        ?.createInputStream() ?: throw IOException("Cannot read source: ${file.filename}")
                    val measured = cancel.reading(input) { stream -> spool.outputStream().use { copy(stream, it, MAX_FILE_BYTES, cancel) } }
                    if (file.size > 0L && measured != file.size) throw IOException("Source size changed: ${file.filename}")
                    id = client.enqueueUploadFile(repoId, UploadPaths.parent(file.relativeDir), file.filename,
                        file.contentType, spool.absolutePath, sources)
                }
                // Durable ID + sources already exist before this first network call.
                queued = true
                toSend[id] = chunk.size
                progress?.invoke()
            } catch (e: Exception) {
                cancel.check()
                // Splitting is safe ONLY before an intent was queued. Never
                // create new IDs for a pack whose outcome may be uncertain.
                if (!queued && chunk.size > 1) {
                    chunk.forEach { sendChunk(listOf(it), tree) }
                } else {
                    if (queued) result.waiting += chunk.size
                    result.errors += "${chunk.first().filename}: ${e.message}"
                }
            } finally { spool?.delete() }
        }
        val chunks = if (packed) FolderPreflight.chunkBySize(fresh) else fresh.map { listOf(it) }
        chunks.forEach { sendChunk(it, packed) }
        // Queue every readable source before network I/O so a cancelled large
        // transfer cannot prevent the remaining files from entering the queue.
        if (queueOnly) result.waiting += toSend.values.sum()
        else for ((id, count) in toSend) {
            cancel.check()
            try { account(PendingUpload.listFromJson(client.sendUploadJSON(repoId,id)).single(),count) }
            catch (e: Exception) { cancel.check(); result.waiting += count; result.errors += e.message ?: "Capture failed" }
        }
        result
    }
}
