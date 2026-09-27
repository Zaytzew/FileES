package net.filees.mobile

import android.content.ContentResolver
import java.io.File
import java.io.InputStream
import java.io.IOException
import java.util.zip.CRC32
import java.util.zip.ZipEntry
import java.util.zip.ZipOutputStream

/** Zip-on-wire packer. Lives only in cacheDir; sweep leftovers on launch. */
object TreeZip {
    // Must match pkg/mobile/v1.TreePackComment. The worker unpacks only this.
    const val COMMENT = "filees.tree/v1"

    // A STORED zip entry needs its CRC32/size known before putNextEntry, so a
    // stored file is read twice (once to hash, once to copy) - both passes
    // stream through this fixed buffer. Loading a whole file into one
    // ByteArray here used to throw OutOfMemoryError on anything above roughly
    // a few hundred MB (live, 2026-09-26: a >1 GB video in a year-deep watch
    // backlog asked for a single 1 116 709 344-byte allocation and lost).
    // That Error is not an Exception, so it skipped every catch(Exception) on
    // the way up (TreeZip -> FileesWatchTick.run -> FileesWatchWorker) and
    // reached WorkManager's own handler invisibly - no journal entry, no
    // notification, the file staying unseen forever so every following tick
    // repeated the identical crash.
    private const val BUFFER_SIZE = 64 * 1024

    private val storeExt = setOf(
        "jpg", "jpeg", "png", "gif", "webp", "heic",
        "mp4", "mov", "m4v", "avi",
        "mp3", "m4a", "ogg", "wav", "flac",
        "zip", "gz", "bz2", "xz", "7z", "rar",
        "pdf",
    )

    fun packNamed(files: List<Pair<String, File>>, cacheDir: File, zipName: String): File {
        cacheDir.mkdirs()
        val out = File(cacheDir, zipName)
        try {
            ZipOutputStream(out.outputStream().buffered()).use { zip ->
                for ((name, file) in files) {
                    val entry = ZipEntry(name)
                    if (stored(name.substringAfterLast('/'))) {
                        entry.method = ZipEntry.STORED
                        entry.size = file.length()
                        entry.compressedSize = file.length()
                        entry.crc = file.inputStream().use { crcOf(it) }
                    } else {
                        entry.method = ZipEntry.DEFLATED
                    }
                    zip.putNextEntry(entry)
                    file.inputStream().use { it.copyTo(zip, BUFFER_SIZE) }
                    zip.closeEntry()
                }
            }
        } catch (e: Exception) {
            out.delete()
            throw e
        }
        return out
    }

    fun pack(resolver: ContentResolver, files: List<WalkedFile>, cacheDir: File, cancel: CaptureCancellation = CaptureCancellation()): File {
        require(files.size <= FolderPreflight.MAX_CHUNK_FILES)
        var remaining = CaptureTransfers.MAX_FILE_BYTES
        cacheDir.mkdirs()
        val out = File.createTempFile("pack-", ".zip", cacheDir)
        try {
            ZipOutputStream(out.outputStream().buffered()).use { zip ->
                zip.setComment(COMMENT)
                for (file in files) {
                    cancel.check()
                    val name = listOf(file.relativeDir.trim('/'), file.filename)
                        .filter { it.isNotBlank() }
                        .joinToString("/")
                    val entry = ZipEntry(name)
                    entry.time = 0L // deterministic pack on retries
                    if (stored(file.filename)) {
                        // SAF documents do not reliably report length() the way
                        // a local File does, so size comes from this same
                        // hashing pass rather than a second, provider-specific
                        // length query that could return UNKNOWN_LENGTH.
                        val (crc, size) = cancel.reading(resolver.openAssetFileDescriptor(file.uri, "r", cancel.signal)?.createInputStream() ?: throw IOException("Cannot read source: ${file.filename}")) { crcAndSizeOf(it, remaining, cancel) }
                        entry.method = ZipEntry.STORED
                        entry.size = size
                        entry.compressedSize = size
                        entry.crc = crc
                    } else {
                        entry.method = ZipEntry.DEFLATED
                    }
                    cancel.reading(resolver.openAssetFileDescriptor(file.uri, "r", cancel.signal)?.createInputStream() ?: throw IOException("Cannot read source: ${file.filename}")) { input ->
                        zip.putNextEntry(entry)
                        val measured = CaptureTransfers.copy(input, zip, remaining, cancel)
                        if (file.size > 0L && measured != file.size) throw IOException("Source size changed: ${file.filename}")
                        remaining -= measured
                        zip.closeEntry()
                    }
                }
            }
        } catch (e: Exception) {
            out.delete()
            throw e
        }
        return out
    }

    private fun crcOf(input: InputStream): Long = crcAndSizeOf(input).first

    private fun crcAndSizeOf(input: InputStream, limit: Long = Long.MAX_VALUE, cancel: CaptureCancellation = CaptureCancellation()): Pair<Long, Long> {
        val crc = CRC32()
        val buf = ByteArray(BUFFER_SIZE)
        var size = 0L
        while (true) {
            cancel.check()
            val n = input.read(buf)
            if (n < 0) break
            crc.update(buf, 0, n)
            size += n
            if (size > limit) throw IOException("Capture exceeds size limit ($limit bytes)")
        }
        return crc.value to size
    }

    fun sweep(cacheDir: File) {
        cacheDir.listFiles { f ->
            f.isFile && f.name.startsWith("pack-") && f.name.endsWith(".zip")
        }?.forEach { it.delete() }
    }

    private fun stored(filename: String): Boolean {
        val ext = filename.substringAfterLast('.', "").lowercase()
        return ext in storeExt
    }
}
