package net.filees.mobile

import android.app.Instrumentation
import android.content.ContentProvider
import android.content.ContentValues
import android.database.Cursor
import android.net.Uri
import android.os.Bundle
import android.os.ParcelFileDescriptor
import androidbind.Androidbind
import java.io.ByteArrayInputStream
import java.io.ByteArrayOutputStream
import java.io.File
import java.io.FileNotFoundException
import java.io.IOException
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit
import java.util.zip.ZipFile

class CaptureInstrumentation : Instrumentation() {
    private var args = Bundle()
    override fun onCreate(arguments: Bundle?) { super.onCreate(arguments); args = arguments ?: Bundle(); start() }
    override fun onStart() {
        val results = Bundle()
        try {
            // Instrumentation.start() races Application.onCreate(); wait for
            // its startup cache sweep before producing test payloads.
            waitForIdleSync()
            if (args.containsKey("watch_bindings")) {
                WatchRegistryChecks.run()
                WatchBindingAndroidChecks.run(targetContext)
            } else if (args.containsKey("ui_requests")) {
                UiRequestChecks.run()
            } else if (args.containsKey("watch_worker")) {
                WatchForegroundChecks.run(this,args.getString("host_key")!!)
            } else if (args.containsKey("ui_checks")) {
                WatchStatusChecks.run(this)
            } else if (args.containsKey("process_phase")) {
                testAcrossProcessRestart(args.getString("process_phase")!!)
            } else {
                testBatchLimits()
                testStreamingAndCompleteness()
                testCancellationAndCoordinator()
                if (args.containsKey("host_key")) testDurableRoundTrip()
            }
            results.putString("stream", "Capture robustness instrumentation PASS\n")
            finish(-1, results)
        } catch (t: Throwable) {
            results.putString("stream", t.stackTraceToString())
            finish(1, results)
        }
    }
    private fun source(bytes: Long) = Uri.parse("content://net.filees.mobile.test.capture/$bytes")
    private fun testBatchLimits() {
        val files = (1..5001).map { WalkedFile(source(1),"","$it.bin","",1) }
        val chunks = FolderPreflight.chunkBySize(files)
        check(chunks.size == 6 && chunks.sumOf { it.size } == 5001)
        check(chunks.all { it.size <= FolderPreflight.MAX_CHUNK_FILES })
        check(FolderPreflight.chunkBySize(listOf(files[0],files[1].copy(size=0),files[2])).size == 3)
        val cancelled = CaptureCancellation(); cancelled.cancel()
        check(runCatching { CaptureTransfers.copy(ByteArrayInputStream(ByteArray(1)),ByteArrayOutputStream(),2,cancelled) }.isFailure)
        check(runCatching { CaptureTransfers.copy(ByteArrayInputStream(ByteArray(17)),ByteArrayOutputStream(),16,CaptureCancellation()) }.isFailure)
    }
    private fun testStreamingAndCompleteness() {
        val dir = File(targetContext.cacheDir,"capture-test-${System.nanoTime()}").apply { mkdirs() }
        try {
            val bytes = 128L * 1024 * 1024
            val file = WalkedFile(source(bytes),"test","large.mp4","",0)
            val zip = TreeZip.pack(targetContext.contentResolver,listOf(file),dir)
            ZipFile(zip).use { archive ->
                check(archive.comment == TreeZip.COMMENT)
                val entry = archive.entries().nextElement()
                check(entry.size == bytes)
                var read = 0L
                archive.getInputStream(entry).use { input ->
                    val buffer = ByteArray(64*1024)
                    while (true) { val n=input.read(buffer); if(n<0) break; read+=n }
                }
                check(read == bytes)
            }
            zip.delete()
            check(runCatching { TreeZip.pack(targetContext.contentResolver,listOf(file.copy(uri=source(3),size=4)),dir) }.isFailure)
            check(runCatching { TreeZip.pack(targetContext.contentResolver,listOf(file.copy(uri=Uri.parse("content://net.filees.mobile.test.capture/missing"))),dir) }.isFailure)
            check(dir.listFiles().orEmpty().isEmpty())
        } finally { dir.deleteRecursively() }
    }
    private fun testCancellationAndCoordinator() {
        val held = CountDownLatch(1); val release=CountDownLatch(1); val acquired=CountDownLatch(1)
        val first=Thread { CaptureCoordinator.run(CaptureCancellation()) { held.countDown(); check(release.await(5,TimeUnit.SECONDS)) } }
        first.start(); check(held.await(5,TimeUnit.SECONDS))
        val token=CaptureCancellation()
        val second=Thread { try { CaptureCoordinator.run(token) { acquired.countDown() } } catch (_: IOException) {} }
        second.start(); token.cancel(); second.join(2000)
        check(!second.isAlive && acquired.count==1L)
        release.countDown(); first.join(2000)
        // Cancelling a blocked SAF pipe closes the actual descriptor.
        val pipe=ParcelFileDescriptor.createPipe(); val reading=CountDownLatch(1)
        val cancel=CaptureCancellation()
        val reader=Thread { try { cancel.reading(ParcelFileDescriptor.AutoCloseInputStream(pipe[0])) { reading.countDown(); it.read() } } catch (_: IOException) {} }
        reader.start(); check(reading.await(2,TimeUnit.SECONDS)); cancel.cancel(); reader.join(2000); val stopped = !reader.isAlive; pipe[1].close(); check(stopped)
    }
    private fun testDurableRoundTrip() {
        val store=File(targetContext.filesDir,"capture-e2e-${System.nanoTime()}").apply { mkdirs() }
        val client=Androidbind.newClient(store.absolutePath,"10.0.2.2:22380","test",args.getString("host_key"))
        val file=WalkedFile(source(4096),"e2e","note.bin","",4096)
        val zip=TreeZip.pack(targetContext.contentResolver,listOf(file),targetContext.cacheDir)
        try {
            val id=client.enqueueTreeFile("repo-1",UploadPaths.ROOT,1,zip.absolutePath,"[\"instrumented-source\"]")
            val first=PendingUpload.listFromJson(client.sendUploadJSON("repo-1",id)).single()
            check(!first.delivered) // harness commits, then loses ACK and status
            val restarted=Androidbind.newClient(store.absolutePath,"10.0.2.2:22380","test",args.getString("host_key"))
            val receipt=PendingUpload.listFromJson(restarted.sendUploadJSON("repo-1",id)).single()
            check(receipt.delivered && receipt.id==id)
            check(PendingUpload.listFromJson(restarted.listUploadsJSON("repo-1")).size==1)
            check(!File(store,"uploads/repo-1/$id.bin").exists())
        } finally { zip.delete() }
    }
    private fun testAcrossProcessRestart(phase: String) {
        val store = File(targetContext.filesDir, "capture-process-e2e").apply { mkdirs() }
        val idFile = File(store, "test-pending-id")
        val client = Androidbind.newClient(store.absolutePath,"10.0.2.2:22380","test",args.getString("host_key"))
        if (phase == "prepare") {
            check(!idFile.exists())
            val file = WalkedFile(source(8192),"process","note.bin","",8192)
            val zip = TreeZip.pack(targetContext.contentResolver,listOf(file),targetContext.cacheDir)
            try {
                val id = client.enqueueTreeFile("repo-1",UploadPaths.ROOT,1,zip.absolutePath,"[\"process-source\"]")
                val uncertain = PendingUpload.listFromJson(client.sendUploadJSON("repo-1",id)).single()
                check(!uncertain.delivered)
                idFile.writeText(id)
                check(File(store,"uploads/repo-1/$id.bin").exists())
            } finally { zip.delete() }
        } else {
            check(phase == "resume")
            val id = idFile.readText()
            val queued = PendingUpload.listFromJson(client.listUploadsJSON("repo-1")).single()
            check(queued.id == id && !queued.delivered)
            val receipt = PendingUpload.listFromJson(client.sendUploadJSON("repo-1",id)).single()
            check(receipt.delivered && receipt.id == id)
            check(!File(store,"uploads/repo-1/$id.bin").exists())
            idFile.delete()
        }
    }

}
