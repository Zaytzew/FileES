package net.filees.mobile

import android.app.PendingIntent
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.pm.PackageInstaller
import android.net.Uri
import android.os.Build
import android.provider.Settings
import org.json.JSONObject
import java.io.File
import java.net.HttpURLConnection
import java.net.URL
import java.security.MessageDigest

/** Signed beta channel, the same one the desktop uses. Not Play. */
object ApkUpdate {
    const val PREF_SEQUENCE = "mobile_update_sequence"
    const val EXTRA_SEQUENCE = "filees_update_sequence"

    data class Offer(
        val state: String,
        val message: String,
        val version: String,
        val sequence: Long,
        val sha256: String,
        val size: Long,
        val url: String,
    )

    fun inspect(context: Context): Offer {
        val prefs = context.getSharedPreferences(FileesSession.PREFS, Context.MODE_PRIVATE)
        val version = versionName(context)
        val remembered = prefs.getLong(PREF_SEQUENCE, 0L)
        val raw = androidbind.Androidbind.inspectMobileUpdate(version, remembered)
        val json = JSONObject(raw)
        return Offer(
            state = json.optString("state"),
            message = json.optString("message"),
            version = json.optString("version"),
            sequence = json.optLong("sequence"),
            sha256 = json.optString("sha256"),
            size = json.optLong("size"),
            url = json.optString("url"),
        )
    }

    fun download(context: Context, offer: Offer): File {
        if (offer.url.isBlank() || offer.size <= 0 || offer.sha256.length != 64) {
            throw IllegalStateException("incomplete update offer")
        }
        val dest = File(context.cacheDir, "filees-update.apk")
        val conn = URL(offer.url).openConnection() as HttpURLConnection
        conn.connectTimeout = 20_000
        conn.readTimeout = 120_000
        conn.instanceFollowRedirects = true
        conn.inputStream.use { input -> dest.outputStream().use { input.copyTo(it) } }
        if (dest.length() != offer.size) {
            dest.delete()
            throw IllegalStateException("apk size ${dest.length()} != ${offer.size}")
        }
        val digest = MessageDigest.getInstance("SHA-256").digest(dest.readBytes())
        val hex = digest.joinToString("") { "%02x".format(it) }
        if (hex != offer.sha256) {
            dest.delete()
            throw IllegalStateException("apk hash does not match the signed manifest")
        }
        return dest
    }

    fun archiveVersionCode(context: Context, apk: File): Long {
        val info = context.packageManager.getPackageArchiveInfo(apk.absolutePath, 0) ?: return 0L
        return if (Build.VERSION.SDK_INT >= 28) info.longVersionCode else @Suppress("DEPRECATION") info.versionCode.toLong()
    }

    fun canInstall(context: Context): Boolean {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.O) return true
        return context.packageManager.canRequestPackageInstalls()
    }

    fun unknownSourcesSettings(context: Context): Intent =
        Intent(Settings.ACTION_MANAGE_UNKNOWN_APP_SOURCES, Uri.parse("package:${context.packageName}"))

    fun install(context: Context, apk: File, sequence: Long) {
        val installer = context.packageManager.packageInstaller
        val params = PackageInstaller.SessionParams(PackageInstaller.SessionParams.MODE_FULL_INSTALL)
        val sessionId = installer.createSession(params)
        installer.openSession(sessionId).use { session ->
            session.openWrite("filees.apk", 0, apk.length()).use { out ->
                apk.inputStream().use { it.copyTo(out) }
                session.fsync(out)
            }
            val flags = PendingIntent.FLAG_UPDATE_CURRENT or
                if (Build.VERSION.SDK_INT >= 31) PendingIntent.FLAG_MUTABLE else 0
            val intent = Intent(context, UpdateInstallReceiver::class.java).putExtra(EXTRA_SEQUENCE, sequence)
            val callback = PendingIntent.getBroadcast(context, sessionId, intent, flags)
            session.commit(callback.intentSender)
        }
    }

    private fun versionName(context: Context): String {
        return try {
            val info = context.packageManager.getPackageInfo(context.packageName, 0)
            info.versionName ?: ""
        } catch (_: Exception) {
            ""
        }
    }
}

class UpdateInstallReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        val status = intent.getIntExtra(PackageInstaller.EXTRA_STATUS, PackageInstaller.STATUS_FAILURE)
        when (status) {
            PackageInstaller.STATUS_PENDING_USER_ACTION -> {
                val confirm = if (Build.VERSION.SDK_INT >= 33) {
                    intent.getParcelableExtra(Intent.EXTRA_INTENT, Intent::class.java)
                } else {
                    @Suppress("DEPRECATION")
                    intent.getParcelableExtra(Intent.EXTRA_INTENT)
                }
                confirm?.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
                if (confirm != null) context.startActivity(confirm)
            }
            PackageInstaller.STATUS_SUCCESS -> {
                val sequence = intent.getLongExtra(ApkUpdate.EXTRA_SEQUENCE, 0L)
                if (sequence > 0) {
                    context.getSharedPreferences(FileesSession.PREFS, Context.MODE_PRIVATE)
                        .edit()
                        .putLong(ApkUpdate.PREF_SEQUENCE, sequence)
                        .apply()
                }
            }
        }
    }
}
