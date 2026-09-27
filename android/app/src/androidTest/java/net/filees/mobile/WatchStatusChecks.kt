package net.filees.mobile

import android.app.Instrumentation
import android.content.Intent
import android.net.Uri
import android.view.View
import android.widget.TextView

object WatchStatusChecks {
    fun run(test: Instrumentation) {
        val context = test.targetContext
        check(!AutoUpdate.enabled(context))
        val apk = java.io.File(context.cacheDir,"test-update-integrity.apk")
        try {
            apk.writeText("verified update fixture")
            val hash = java.security.MessageDigest.getInstance("SHA-256").digest(apk.readBytes()).joinToString("") { "%02x".format(it) }
            val offer = ApkUpdate.Offer("available","","0.1.18+r1",1,hash,apk.length(),"https://example.invalid/test.apk")
            check(ApkUpdate.verify(apk,offer))
            check(!ApkUpdate.verify(apk,offer.copy(size=offer.size+1)))
            apk.writeText("corrupted update fixture")
            check(!ApkUpdate.verify(apk,offer))
            AutoUpdate.saveReady(context,offer)
            check(AutoUpdate.ready(context)?.sha256 == hash)
            AutoUpdate.clearReady(context)
            check(AutoUpdate.ready(context) == null)
            val preferences = context.getSharedPreferences(FileesSession.PREFS,0)
            var notices = 0
            try {
                // Exercise the worker engine without scheduling a real channel fetch.
                preferences.edit().putBoolean("android_auto_update",true).commit()
                repeat(2) { AutoUpdate.prepare(context,offer,{ false },{ apk },{ true },{ notices++ }) }
                check(notices == 1 && AutoUpdate.ready(context)?.sha256 == hash)
                AutoUpdate.clearReady(context)
                AutoUpdate.prepare(context,offer,{ false },{
                    preferences.edit().putBoolean("android_auto_update",false).commit(); apk
                },{ true },{ notices++ })
                check(AutoUpdate.ready(context) == null && notices == 1)
                AutoUpdate.prepare(context,offer,{ false },{ error("disabled must not download") },{ true })
            } finally {
                preferences.edit().putBoolean("android_auto_update",false).commit()
                AutoUpdate.clearReady(context)
            }
            val cancel = ApkUpdate.DownloadCancellation(); cancel.cancel()
            check(runCatching { ApkUpdate.download(context,offer,cancel) }.exceptionOrNull() is java.io.InterruptedIOException)
        } finally { apk.delete() }
        val store = WatchStatusStore(context)
        val scope = "test-${System.nanoTime()}"
        val tree = Uri.parse("content://test.documents/tree/root")
        val other = Uri.parse("content://test.documents/tree/other")
        val source = "content://test.documents/tree/root/document/root%2Fa.txt/a.txt"
        check(WatchStatusStore.belongs(source,tree) && !WatchStatusStore.belongs(source,other))
        check(store.state(scope,tree).phase == "unknown")
        store.updateQueue(scope,listOf(tree),emptyList())
        check(store.state(scope,tree).phase == "unknown") // empty != complete
        val token = store.begin(scope,listOf(tree))
        check(store.state(scope,tree).phase == "checking")
        store.scanned(scope,tree)
        val item = PendingUpload("id","mobile-uploads","a.txt",1,"pending-create","","","","",listOf(source))
        store.updateQueue(scope,listOf(tree),listOf(item),finish=true)
        check(store.state(scope,tree).phase == "waiting" && store.state(scope,tree).waiting == 1)
        store.phase(scope,tree,"sending")
        check(store.state(scope,tree).phase == "sending")
        store.end(token) // equivalent to a persisted running token absent after process death
        check(store.state(scope,tree).phase == "waiting")
        store.updateQueue(scope,listOf(tree),listOf(item.copy(state="conflict")),finish=true)
        check(store.state(scope,tree).phase == "error")
        store.updateQueue(scope,listOf(tree),listOf(item.copy(state="committed")),finish=true)
        check(store.state(scope,tree).phase == "complete")
        check(store.state("other-target",tree).phase == "unknown")
        store.problem(scope,tree,"provider unavailable")
        store.updateQueue(scope,listOf(tree),emptyList(),finish=true)
        check(store.state(scope,tree).phase == "error")
        // Unknown provenance in pre-upgrade metadata cannot paint the folder green.
        val next = store.begin(scope,listOf(tree)); store.scanned(scope,tree)
        store.updateQueue(scope,listOf(tree),listOf(item.copy(sources=emptyList())),finish=true)
        check(store.state(scope,tree).phase == "waiting"); store.end(next)
        // Settings use the actual activity/binding and retain every advanced action.
        val activity = test.startActivitySync(Intent(context,SettingsActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
        test.waitForIdleSync()
        test.runOnMainSync {
            val panel = activity.findViewById<View>(R.id.panelAdvanced)
            check(panel.visibility == View.GONE)
            activity.findViewById<View>(R.id.buttonAdvanced).performClick()
            check(panel.visibility == View.VISIBLE && panel.parent != null)
            check(panel.findViewById<View>(R.id.buttonPairPasted) != null)
            check(panel.findViewById<View>(R.id.buttonRequestDesktopJoin) != null)
            check(context.getString(R.string.device_lock_copy).contains("SSH").not())
        }
        test.sendKeyDownUpSync(android.view.KeyEvent.KEYCODE_BACK)
        test.waitForIdleSync()
        test.runOnMainSync {
            check(activity.findViewById<View>(R.id.panelAdvanced).visibility == View.GONE)
            activity.findViewById<View>(R.id.buttonAbout).performClick()
        }
        test.waitForIdleSync()
        test.sendKeyDownUpSync(android.view.KeyEvent.KEYCODE_BACK)
        test.runOnMainSync { activity.finish() }
    }
}
