package net.filees.mobile

import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.view.View
import androidx.appcompat.app.AppCompatActivity
import androidx.core.content.edit
import androidbind.Androidbind
import androidbind.Client
import net.filees.mobile.databinding.ActivityMainBinding
import org.json.JSONObject
import java.util.concurrent.Executors

/**
 * The activation + repository screen. This is a direct-connect placeholder
 * for the real onboarding flow: concept doc §4.2 wants ticket + OTP +
 * reverse-tunnel activation exactly like the desktop daemon's push deploy.
 * That protocol has no Go-side port yet (pkg/deploy's helper/worker dance is
 * desktop-only so far) -- building an OTP screen against a backend call that
 * doesn't exist would just be UI to throw away. Instead, "activation" here
 * means: the device generates its own persistent identity (as it always
 * would), the operator pins the server's address and host key by hand, and
 * the resulting device public key is shown for the operator to authorize
 * server-side (today, by hand -- see SESSION_HANDOFF.md §17 for the
 * Etap 4b stub authority this talks to). The OTP screen slots in later,
 * in front of this same androidbind.Client wiring, once the real protocol
 * exists.
 */
class MainActivity : AppCompatActivity() {

    private lateinit var binding: ActivityMainBinding
    private val io = Executors.newSingleThreadExecutor()
    private val main = Handler(Looper.getMainLooper())

    private var client: Client? = null

    private val prefs by lazy { getSharedPreferences("filees_connection", MODE_PRIVATE) }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        binding = ActivityMainBinding.inflate(layoutInflater)
        setContentView(binding.root)

        binding.editAlias.setText(prefs.getString(PREF_ALIAS, ""))
        binding.editAddress.setText(prefs.getString(PREF_ADDRESS, ""))
        binding.editHostKey.setText(prefs.getString(PREF_HOST_KEY, ""))

        binding.buttonActivate.setOnClickListener { onActivateClicked() }
        binding.buttonRefresh.setOnClickListener { onRefreshClicked() }

        // A prior activation persists its identity under filesDir regardless
        // of whether this Activity is still alive (concept doc §9.2); if we
        // have the connection details saved, reconnect silently so a rotated
        // screen or reopened app does not force the operator to reactivate.
        val savedAddress = prefs.getString(PREF_ADDRESS, null)
        val savedHostKey = prefs.getString(PREF_HOST_KEY, null)
        if (!savedAddress.isNullOrBlank() && !savedHostKey.isNullOrBlank()) {
            activate(savedAddress, savedHostKey, silent = true)
        } else {
            setStatus(getString(R.string.status_idle, getString(R.string.action_activate)))
        }
    }

    private fun onActivateClicked() {
        val alias = binding.editAlias.text?.toString()?.trim().orEmpty()
        val address = binding.editAddress.text?.toString()?.trim().orEmpty()
        val hostKey = binding.editHostKey.text?.toString()?.trim().orEmpty()
        if (address.isEmpty() || hostKey.isEmpty()) {
            setStatus(getString(R.string.status_error, "adres i host key są wymagane"))
            return
        }
        prefs.edit {
            putString(PREF_ALIAS, alias)
            putString(PREF_ADDRESS, address)
            putString(PREF_HOST_KEY, hostKey)
        }
        activate(address, hostKey, silent = false)
    }

    private fun activate(address: String, hostKey: String, silent: Boolean) {
        if (!silent) setStatus(getString(R.string.status_activating))
        io.execute {
            try {
                // MOBILE_USER matches the _filees-mobile SSH class from Etap
                // 4b (SESSION_HANDOFF.md §17) -- it is not operator-editable,
                // since the technical account is fixed by the server class,
                // not chosen by the client.
                val newClient = Androidbind.newClient(filesDir.absolutePath, address, MOBILE_USER, hostKey)
                main.post {
                    client = newClient
                    val alias = binding.editAlias.text?.toString()?.trim().orEmpty()
                    setStatus(getString(R.string.status_activated, alias.ifEmpty { address }, address))
                    binding.labelDevicePublicKey.visibility = View.VISIBLE
                    binding.textDevicePublicKey.visibility = View.VISIBLE
                    binding.textDevicePublicKey.text = newClient.publicKey()
                }
            } catch (e: Exception) {
                main.post { setStatus(getString(R.string.status_error, e.message ?: e.toString())) }
            }
        }
    }

    private fun onRefreshClicked() {
        val active = client
        val repoId = binding.editRepoId.text?.toString()?.trim().orEmpty()
        if (active == null) {
            setStatus(getString(R.string.status_error, "aktywuj klienta najpierw"))
            return
        }
        if (repoId.isEmpty()) {
            setStatus(getString(R.string.status_error, "podaj ID repozytorium"))
            return
        }
        setStatus(getString(R.string.status_refreshing))
        io.execute {
            try {
                val manifestJson = active.refreshJSON(repoId)
                main.post {
                    setStatus(getString(R.string.status_activated, repoId, binding.editAddress.text.toString()))
                    binding.textManifest.text = formatManifest(manifestJson)
                }
            } catch (e: Exception) {
                main.post { setStatus(getString(R.string.status_error, e.message ?: e.toString())) }
            }
        }
    }

    private fun formatManifest(manifestJson: String): String {
        if (manifestJson.isEmpty()) {
            return "(brak zmian od ostatniego odświeżenia)"
        }
        val manifest = JSONObject(manifestJson)
        val entries = manifest.optJSONArray("entries") ?: return "(pusty manifest)"
        val builder = StringBuilder()
        builder.append("revision=").append(manifest.optLong("repo_revision"))
        builder.append(" generation=").append(manifest.optLong("view_generation")).append('\n')
        for (i in 0 until entries.length()) {
            val entry = entries.getJSONObject(i)
            builder.append(if (entry.optString("kind") == "directory") "d " else "  ")
            builder.append(entry.optString("path"))
            if (entry.optString("kind") != "directory") {
                builder.append(" (").append(entry.optLong("size")).append(" B)")
            }
            builder.append('\n')
        }
        return builder.toString()
    }

    private fun setStatus(text: String) {
        binding.textStatus.text = text
    }

    companion object {
        private const val MOBILE_USER = "_filees-mobile"
        private const val PREF_ALIAS = "alias"
        private const val PREF_ADDRESS = "address"
        private const val PREF_HOST_KEY = "host_public_key"
    }
}
