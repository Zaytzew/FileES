package net.filees.mobile

import java.io.ByteArrayInputStream
import java.io.ByteArrayOutputStream
import java.io.File
import java.io.IOException
import java.io.OutputStream

internal object DownloadPublicationChecks {
    fun run(dir: File) {
        for (fault in listOf("none", "create", "open", "input", "copy", "close", "commit")) {
            var deleted = false
            var committed = false
            val result = runCatching {
                DownloadPublication.pending(
                    input = { if (fault == "input") throw IOException("source"); ByteArrayInputStream(byteArrayOf(1, 2, 3)) },
                    create = { if (fault == "create") null else "entry" },
                    open = {
                        if (fault == "open") null else object : OutputStream() {
                            val output = ByteArrayOutputStream()
                            override fun write(b: Int) { if (fault == "copy") throw IOException("full"); output.write(b) }
                            override fun close() { if (fault == "close") throw IOException("flush"); check(output.size() == 3 || fault in listOf("input", "copy")) }
                        }
                    },
                    commit = { if (fault == "commit") 0 else { committed = true; 1 } },
                    remove = { deleted = true },
                )
            }
            check(result.isSuccess == (fault == "none")) { fault }
            check(committed == (fault == "none")) { fault }
            check(deleted == (fault !in listOf("none", "create"))) { fault }
        }
        val source = File(dir, "source").apply { writeText("new") }
        val publicDir = File(dir, "public").apply { mkdirs() }
        val old = File(publicDir, "report.pdf").apply { writeText("old") }
        val first = DownloadPublication.legacy(source, publicDir, "report.pdf")
        val second = DownloadPublication.legacy(source, publicDir, "report.pdf")
        check(old.readText() == "old" && first.name == "report (1).pdf" && second.name == "report (2).pdf")
        check(first.readText() == "new" && second.readText() == "new")
        check(runCatching { DownloadPublication.legacy(File(dir, "missing"), publicDir, "incomplete.pdf") }.isFailure)
        check(!File(publicDir, "incomplete.pdf").exists())
        for (bad in listOf("", ".", "..", "../x", "a\\x", "x\u0000y")) {
            check(runCatching { DownloadPublication.legacy(source, publicDir, bad) }.isFailure)
        }
        println("DownloadPublication: pending failures, commit, collision, cleanup, names PASS")
    }
}

fun main() {
    val dir = kotlin.io.path.createTempDirectory("filees-publication-").toFile()
    try { DownloadPublicationChecks.run(dir) } finally { dir.deleteRecursively() }
}
