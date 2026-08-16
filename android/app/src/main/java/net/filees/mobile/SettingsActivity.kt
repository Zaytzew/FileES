package net.filees.mobile

import android.Manifest
import android.content.Intent
import android.content.pm.PackageManager
import android.net.Uri
import android.os.Bundle
import android.widget.LinearLayout
import android.widget.TextView
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AppCompatActivity
import androidx.core.content.ContextCompat
import androidx.core.content.edit
import androidbind.Androidbind
import com.google.android.material.button.MaterialButton
import com.journeyapps.barcodescanner.ScanContract
import com.journeyapps.barcodescanner.ScanOptions
import net.filees.mobile.databinding.ActivitySettingsBinding
import org.json.JSONObject

class SettingsActivity : AppCompatActivity() {

    private lateinit var binding: ActivitySettingsBinding
    private lateinit var watched: WatchedFolders

    private val scanLauncher = registerForActivityResult(ScanContract()) { result ->
        result.contents?.let { pairFromPayload(it) }
    }
    private val cameraPermissionLauncher = registerForActivityResult(ActivityResultContracts.RequestPermission()) { granted ->
        if (granted) launchScanner()
    }
    private val addWatchLauncher = registerForActivityResult(ActivityResultContracts.OpenDocumentTree()) { uri ->
        if (uri != null) {
            contentResolver.takePersistableUriPermission(uri, Intent.FLAG_GRANT_READ_URI_PERMISSION)
            watched.add(uri)
            renderWatched()
            FileesWatchScheduler.runSoon(this)
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        installFileesWindow()
        binding = ActivitySettingsBinding.inflate(layoutInflater)
        setContentView(binding.root)
        setSupportActionBar(binding.toolbar)
        supportActionBar?.setDisplayHomeAsUpEnabled(true)
        binding.toolbar.setNavigationOnClickListener { finish() }
        binding.toolbar.padTopSystemBars()
        binding.scrollSettings.padBottomSystemBars(16)
        watched = WatchedFolders(this)

        binding.buttonPairPasted.setOnClickListener {
            pairFromPayload(binding.editPairingPayload.text?.toString()?.trim().orEmpty())
        }
        binding.buttonScanQr.setOnClickListener {
            if (ContextCompat.checkSelfPermission(this, Manifest.permission.CAMERA) == PackageManager.PERMISSION_GRANTED) {
                launchScanner()
            } else {
                cameraPermissionLauncher.launch(Manifest.permission.CAMERA)
            }
        }
        binding.buttonAddWatched.setOnClickListener { addWatchLauncher.launch(null) }

        val prefs = getSharedPreferences(FileesSession.PREFS, MODE_PRIVATE)
        val address = prefs.getString(FileesSession.PREF_ADDRESS, null)
        val hostKey = prefs.getString(FileesSession.PREF_HOST_KEY, null)
        if (!address.isNullOrBlank() && !hostKey.isNullOrBlank()) {
            try {
                val client = Androidbind.newClient(filesDir.absolutePath, address, FileesSession.MOBILE_USER, hostKey)
                binding.textDevicePublicKey.text = client.publicKey()
            } catch (_: Exception) {
                binding.textDevicePublicKey.text = getString(R.string.label_device_public_key)
            }
        }
        binding.textDetails.text = when {
            !address.isNullOrBlank() -> getString(R.string.settings_server, address)
            else -> prefs.getString(FileesSession.PREF_DETAILS, "")
        }
        renderWatched()
    }

    private fun renderWatched() {
        binding.listWatched.removeAllViews()
        val uris = watched.uris()
        if (uris.isEmpty()) {
            val empty = TextView(this)
            empty.text = getString(R.string.watched_empty)
            binding.listWatched.addView(empty)
            return
        }
        for (uri in uris) {
            val row = LinearLayout(this)
            row.orientation = LinearLayout.HORIZONTAL
            val label = TextView(this)
            label.layoutParams = LinearLayout.LayoutParams(0, LinearLayout.LayoutParams.WRAP_CONTENT, 1f)
            label.text = uri.lastPathSegment ?: uri.toString()
            val remove = MaterialButton(this, null, com.google.android.material.R.attr.materialButtonOutlinedStyle)
            remove.text = getString(R.string.action_remove_watched)
            remove.setOnClickListener {
                watched.remove(uri)
                renderWatched()
            }
            row.addView(label)
            row.addView(remove)
            binding.listWatched.addView(row)
        }
    }

    private fun launchScanner() {
        val options = ScanOptions()
        options.setCaptureActivity(QrCaptureActivity::class.java)
        options.setDesiredBarcodeFormats(ScanOptions.QR_CODE)
        options.setPrompt(getString(R.string.scan_qr_prompt))
        options.setBeepEnabled(false)
        options.setOrientationLocked(true)
        scanLauncher.launch(options)
    }

    private fun pairFromPayload(payload: String) {
        if (payload.isBlank()) return
        val json = try {
            JSONObject(payload)
        } catch (_: Exception) {
            return
        }
        binding.editPairingPayload.text?.clear()
        Thread {
            try {
                Androidbind.pairJSON(
                    filesDir.absolutePath,
                    json.getString("address"),
                    json.getString("host_public_key"),
                    json.getString("token"),
                )
                getSharedPreferences(FileesSession.PREFS, MODE_PRIVATE).edit {
                    putString(FileesSession.PREF_ADDRESS, json.getString("address"))
                    putString(FileesSession.PREF_HOST_KEY, json.getString("host_public_key"))
                }
                runOnUiThread { finish() }
            } catch (_: Exception) {
                // Main screen shows connection errors on resume.
            }
        }.start()
    }
}
