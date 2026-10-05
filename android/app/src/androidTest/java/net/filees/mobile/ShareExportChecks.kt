package net.filees.mobile

import android.content.Context
import android.content.Intent
import android.net.Uri
import java.io.File
import java.util.zip.ZipFile

internal object ShareExportChecks {
    fun run(context: Context) {
        val server = "share-check-${System.nanoTime()}"
        val dir = DownloadCache.create(context.cacheDir, server)
        try {
            val source = File(dir, "plan.bin").apply { writeText("synthetic content") }
            val send = ShareExport.sendIntent(context, source, source.name, folder = false)
            val uri = send.getParcelableExtra<Uri>(Intent.EXTRA_STREAM) ?: error("Missing shared URI")
            check(send.action == Intent.ACTION_SEND)
            check(uri.scheme == "content" && uri == send.clipData?.getItemAt(0)?.uri)
            check(send.flags and Intent.FLAG_GRANT_READ_URI_PERMISSION != 0)
            check(context.contentResolver.openInputStream(uri)?.use { it.readBytes().decodeToString() } == "synthetic content")

            val entries = listOf(
                ManifestEntry("plans/a.dwg", "file", 2),
                ManifestEntry("plans/deep/b.pdf", "file", 3),
            )
            val relative = ShareExport.relativeFiles("plans", entries).map { it.second }
            check(relative == listOf("a.dwg", "deep/b.pdf"))
            for (bad in listOf(
                listOf(ManifestEntry("plans/../secret", "file", 1)),
                listOf(ManifestEntry("plans-extra/secret", "file", 1)),
                listOf(ManifestEntry("plans/sub\\secret", "file", 1)),
                listOf(ManifestEntry("plans/C:/secret", "file", 1)),
                listOf(ManifestEntry("plans/name\nsecret", "file", 1)),
                listOf(ManifestEntry("plans/a.dwg", "file", 1), ManifestEntry("plans/a.dwg", "file", 1)),
            )) check(runCatching { ShareExport.relativeFiles("plans", bad) }.isFailure)

            val zip = TreeZip.packNamed(listOf("deep/plan.bin" to source), dir, "plans.zip")
            val zipIntent = ShareExport.sendIntent(context, zip, zip.name, folder = true)
            check(zipIntent.type == "application/zip")
            val zipUri = zipIntent.getParcelableExtra<Uri>(Intent.EXTRA_STREAM) ?: error("Missing ZIP URI")
            context.contentResolver.openInputStream(zipUri)?.use { check(it.read() == 'P'.code) } ?: error("ZIP unreadable")
            ZipFile(zip).use { archive ->
                check(archive.getEntry("deep/plan.bin") != null)
                check(archive.getEntry("../secret") == null)
            }
            val emptyZip = TreeZip.packNamed(emptyList(), dir, "empty.zip")
            ZipFile(emptyZip).use { check(!it.entries().hasMoreElements()) }
        } finally {
            DownloadCache.removeAttempt(context.cacheDir, dir)
        }
        println("ShareExport: private URI grants, ZIP contents, unsafe paths PASS")
    }
}
