package net.filees.mobile

import android.widget.TextView
import androidx.appcompat.app.AlertDialog
import androidx.appcompat.app.AppCompatActivity

// Share the catalogue across foreground errors, notifications and folder cards.
fun mobileRecoveryMessage(context: android.content.Context, raw: String): String? {
    val text = raw.lowercase()
    if (!text.contains("mobile operation failed: storage.full:") &&
        !text.contains("mobile operation failed: operation.uncertain:")) return null
    return runCatching {
        androidbind.Androidbind.explainIn(raw, context.resources.configuration.locales[0].language)
    }.getOrNull()?.takeIf { it.isNotBlank() }
}

fun AppCompatActivity.showTransportError(headline: String, err: Throwable, address: String?) {
    val raw = err.message?.takeIf { it.isNotBlank() } ?: err.toString()
    val catalog = try {
        val lang = resources.configuration.locales[0].language
        androidbind.Androidbind.explainIn(raw, lang)
    } catch (_: Exception) {
        try {
            androidbind.Androidbind.explain(raw)
        } catch (_: Exception) {
            ""
        }
    }
    val recovery = mobileRecoveryMessage(this, raw)
    val body = buildString {
        append(recovery ?: catalog.ifBlank { explainTransport(raw) })
        if (!address.isNullOrBlank()) {
            append("\n\n")
            append(getString(R.string.error_address, address))
        }
        if (recovery == null) { append("\n\n"); append(raw) }
    }
    val dialog = AlertDialog.Builder(this)
        .setTitle(headline)
        .setMessage(body)
        .setPositiveButton(android.R.string.ok, null)
        .create()
    dialog.setOnShowListener {
        dialog.findViewById<TextView>(android.R.id.message)?.setTextIsSelectable(true)
    }
    dialog.show()
}

private fun AppCompatActivity.explainTransport(raw: String): String {
    val text = raw.lowercase()
    return when {
        "i/o timeout" in text || "deadline exceeded" in text || "timed out" in text ->
            getString(R.string.error_timeout)
        "no such host" in text -> getString(R.string.error_dns)
        "lookup" in text -> getString(R.string.error_timeout)
        "connection refused" in text -> getString(R.string.error_refused)
        "network is unreachable" in text || "no route to host" in text ->
            getString(R.string.error_unreachable)
        "host key mismatch" in text -> getString(R.string.error_host_key)
        "missing port" in text -> getString(R.string.error_missing_port)
        "upload_tree" in text || "not ingested" in text -> getString(R.string.error_tree_unsupported)
        "op.unsupported" in text || "not supported" in text -> getString(R.string.error_op_unsupported)
        else -> getString(R.string.error_generic)
    }
}
