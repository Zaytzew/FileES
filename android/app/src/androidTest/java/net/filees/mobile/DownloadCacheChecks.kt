package net.filees.mobile

import java.io.File

internal object DownloadCacheChecks {
    fun run(root: File) {
        val now = System.currentTimeMillis()
        val old = DownloadCache.create(root, "a")
        File(old, "report.pdf").writeText("old")
        old.setLastModified(now - 25L * 60 * 60 * 1000)
        val first = DownloadCache.create(root, "a")
        val second = DownloadCache.create(root, "a")
        val other = DownloadCache.create(root, "b")
        File(first, "report.pdf").writeText("first")
        File(second, "report.pdf").writeText("second")
        File(other, "report.pdf").writeText("other server")
        check(first != second)
        val legacy = File(root, "dl/legacy.bin").apply { writeText("legacy"); setLastModified(now - 25L * 60 * 60 * 1000) }
        val upload = File(root, "capture-live.tmp").apply { writeText("pending upload") }
        val oldApk = File(root, "filees-update-${"a".repeat(64)}.apk").apply { writeText("obsolete"); setLastModified(now - 25L * 60 * 60 * 1000) }
        val readyApk = File(root, "filees-update-${"b".repeat(64)}.apk").apply { writeText("ready"); setLastModified(now - 25L * 60 * 60 * 1000) }
        DownloadCache.sweep(root, now, readyApk.name)
        check(!old.exists() && !legacy.exists())
        check(!oldApk.exists() && readyApk.exists())
        check(first.exists() && second.exists() && other.exists() && upload.exists())
        DownloadCache.forget(root, "a")
        check(!first.exists() && !second.exists())
        check(File(other, "report.pdf").readText() == "other server")
        check(runCatching { DownloadCache.create(root, "a") }.isFailure)
        check(runCatching { DownloadCache.checkActive(root, "a") }.isFailure)
        val rePaired = DownloadCache.create(root, "new-pair-id")
        check(rePaired.exists())
        DownloadCache.removeAttempt(root, rePaired)
        check(!rePaired.exists() && upload.exists())
        DownloadCache.removeAttempt(root, upload) // outside dl: must be untouched
        check(upload.exists())
        println("DownloadCache: isolated attempts/server, TTL, unpair fence, scope PASS")
    }
}

fun main() {
    val dir = kotlin.io.path.createTempDirectory("filees-cache-").toFile()
    try { DownloadCacheChecks.run(dir) } finally { dir.deleteRecursively() }
}
