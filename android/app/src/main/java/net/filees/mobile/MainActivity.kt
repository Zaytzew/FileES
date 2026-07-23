package net.filees.mobile

import android.Manifest
import android.content.pm.PackageManager
import android.net.Uri
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.provider.OpenableColumns
import android.view.View
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AppCompatActivity
import androidx.core.content.ContextCompat
import androidx.core.content.edit
import androidx.recyclerview.widget.LinearLayoutManager
import androidbind.Androidbind
import androidbind.Client
import com.journeyapps.barcodescanner.ScanContract
import com.journeyapps.barcodescanner.ScanOptions
import net.filees.mobile.databinding.ActivityMainBinding
import org.json.JSONObject
import java.util.concurrent.Executors

/**
 * The activation + repository screen. Mobile never self-activates (concept
 * doc §4.2): a device can only be paired by an already-active desktop
 * installation of the same realm, which mints a short-lived pairing token
 * and displays it as a QR code (address + host public key + token). This
 * screen scans that QR (or accepts the same JSON pasted, as a fallback for
 * testing/accessibility), drives androidbind.Androidbind.pairJSON to push
 * this device's own already-generated public key and complete activation,
 * and only then persists address/host key for future silent reconnects.
 * The pairing token itself is single-use and short-lived and is never
 * written to SharedPreferences.
 */
class MainActivity : AppCompatActivity() {

    private lateinit var binding: ActivityMainBinding
    private val io = Executors.newSingleThreadExecutor()
    private val main = Handler(Looper.getMainLooper())

    private var client: Client? = null
    private var pairedAddress: String? = null
    private val uploadsAdapter = PendingUploadsAdapter(onDiscard = { onDiscardClicked(it) })

    private val prefs by lazy { getSharedPreferences("filees_connection", MODE_PRIVATE) }

    private val pickFileLauncher = registerForActivityResult(ActivityResultContracts.OpenDocument()) { uri ->
        if (uri != null) onFilePicked(uri)
    }

    private val scanLauncher = registerForActivityResult(ScanContract()) { result ->
        val contents = result.contents
        if (contents == null) {
            setStatus(getString(R.string.status_scan_cancelled))
        } else {
            pairFromPayload(contents)
        }
    }

    private val cameraPermissionLauncher = registerForActivityResult(ActivityResultContracts.RequestPermission()) { granted ->
        if (granted) launchScanner() else setStatus(getString(R.string.status_camera_permission_denied))
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        binding = ActivityMainBinding.inflate(layoutInflater)
        setContentView(binding.root)

        binding.recyclerUploads.layoutManager = LinearLayoutManager(this)
        binding.recyclerUploads.adapter = uploadsAdapter

        binding.buttonScanQr.setOnClickListener { onScanQrClicked() }
        binding.buttonPairPasted.setOnClickListener {
            pairFromPayload(binding.editPairingPayload.text?.toString()?.trim().orEmpty())
        }
        binding.buttonRefresh.setOnClickListener { onRefreshClicked() }
        binding.buttonPickFile.setOnClickListener { pickFileLauncher.launch(arrayOf("*/*")) }
        binding.buttonDrain.setOnClickListener { onDrainClicked() }

        // A prior activation persists its identity under filesDir regardless
        // of whether this Activity is still alive (concept doc §9.2); if we
        // have the connection details saved, reconnect silently so a rotated
        // screen or reopened app does not force the operator to re-pair.
        val savedAddress = prefs.getString(PREF_ADDRESS, null)
        val savedHostKey = prefs.getString(PREF_HOST_KEY, null)
        if (!savedAddress.isNullOrBlank() && !savedHostKey.isNullOrBlank()) {
            activate(savedAddress, savedHostKey, silent = true)
        } else {
            setStatus(getString(R.string.status_idle))
        }
    }

    private fun onScanQrClicked() {
        if (ContextCompat.checkSelfPermission(this, Manifest.permission.CAMERA)
            == PackageManager.PERMISSION_GRANTED
        ) {
            launchScanner()
        } else {
            cameraPermissionLauncher.launch(Manifest.permission.CAMERA)
        }
    }

    private fun launchScanner() {
        val options = ScanOptions()
        options.setDesiredBarcodeFormats(ScanOptions.QR_CODE)
        options.setBeepEnabled(false)
        options.setOrientationLocked(true)
        scanLauncher.launch(options)
    }

    private fun pairFromPayload(payload: String) {
        if (payload.isBlank()) {
            setStatus(getString(R.string.status_error, "brak danych parowania"))
            return
        }
        val address: String
        val hostKey: String
        val token: String
        try {
            val json = JSONObject(payload)
            address = json.getString("address")
            hostKey = json.getString("host_public_key")
            token = json.getString("token")
        } catch (e: Exception) {
            setStatus(getString(R.string.status_error, "nieprawidłowe dane parowania"))
            return
        }
        pairAndActivate(address, hostKey, token)
    }

    private fun pairAndActivate(address: String, hostKey: String, token: String) {
        setStatus(getString(R.string.status_pairing))
        io.execute {
            try {
                // PairJSON drives push -> prove -> finish against this
                // device's own already-persisted identity (concept doc
                // §4.2); the token is single-use and short-lived, so it is
                // consumed here and never saved.
                Androidbind.pairJSON(filesDir.absolutePath, address, hostKey, token)
                prefs.edit {
                    putString(PREF_ADDRESS, address)
                    putString(PREF_HOST_KEY, hostKey)
                }
                main.post { activate(address, hostKey, silent = false) }
            } catch (e: Exception) {
                main.post { setStatus(getString(R.string.status_error, e.message ?: e.toString())) }
            }
        }
    }

    private fun activate(address: String, hostKey: String, silent: Boolean) {
        if (!silent) setStatus(getString(R.string.status_activating))
        io.execute {
            try {
                // MOBILE_USER matches the _filees-mobile SSH class (concept
                // doc §4.2) -- it is not operator-editable, since the
                // technical account is fixed by the server class, not
                // chosen by the client.
                val newClient = Androidbind.newClient(filesDir.absolutePath, address, MOBILE_USER, hostKey)
                main.post {
                    client = newClient
                    pairedAddress = address
                    setStatus(getString(R.string.status_activated, address))
                    binding.labelDevicePublicKey.visibility = View.VISIBLE
                    binding.textDevicePublicKey.visibility = View.VISIBLE
                    binding.textDevicePublicKey.text = newClient.publicKey()
                    // The queue lives in the local Store, independent of this
                    // Activity's lifecycle (concept doc §9.2) -- show whatever
                    // was already queued from a prior session immediately.
                    refreshUploadsList()
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
                    setStatus(getString(R.string.status_activated, pairedAddress ?: repoId))
                    binding.textManifest.text = formatManifest(manifestJson)
                    refreshUploadsList()
                }
            } catch (e: Exception) {
                main.post { setStatus(getString(R.string.status_error, e.message ?: e.toString())) }
            }
        }
    }

    private fun onFilePicked(uri: Uri) {
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
        val parentPath = binding.editParentPath.text?.toString()?.trim().orEmpty()
        val (filename, contentType) = queryFileMeta(uri)
        io.execute {
            try {
                val bytes = contentResolver.openInputStream(uri)?.use { it.readBytes() }
                    ?: throw IllegalStateException("nie można odczytać pliku")
                // EnqueueUpload durably queues the candidate before any
                // network attempt (concept doc §9.2) -- the picker's job ends
                // here; DrainPending is a separate, explicit step.
                active.enqueueUpload(repoId, parentPath, filename, contentType, bytes)
                main.post {
                    setStatus("Dodano do kolejki: $filename")
                    refreshUploadsList()
                }
            } catch (e: Exception) {
                main.post { setStatus(getString(R.string.status_error, e.message ?: e.toString())) }
            }
        }
    }

    private fun queryFileMeta(uri: Uri): Pair<String, String> {
        var name = uri.lastPathSegment ?: "upload.bin"
        contentResolver.query(uri, arrayOf(OpenableColumns.DISPLAY_NAME), null, null, null)?.use { cursor ->
            val index = cursor.getColumnIndex(OpenableColumns.DISPLAY_NAME)
            if (index >= 0 && cursor.moveToFirst()) {
                name = cursor.getString(index) ?: name
            }
        }
        val contentType = contentResolver.getType(uri).orEmpty()
        return name to contentType
    }

    private fun onDrainClicked() {
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
        setStatus("Wysyłanie kolejki…")
        io.execute {
            try {
                active.drainPendingJSON(repoId)
                main.post {
                    setStatus("Kolejka wysłana.")
                    refreshUploadsList()
                }
            } catch (e: Exception) {
                main.post { setStatus(getString(R.string.status_error, e.message ?: e.toString())) }
            }
        }
    }

    private fun onDiscardClicked(item: PendingUpload) {
        val active = client ?: return
        val repoId = binding.editRepoId.text?.toString()?.trim().orEmpty()
        io.execute {
            try {
                active.discardUpload(repoId, item.id)
                main.post { refreshUploadsList() }
            } catch (e: Exception) {
                main.post { setStatus(getString(R.string.status_error, e.message ?: e.toString())) }
            }
        }
    }

    private fun refreshUploadsList() {
        val active = client ?: return
        val repoId = binding.editRepoId.text?.toString()?.trim().orEmpty()
        if (repoId.isEmpty()) return
        io.execute {
            try {
                val listJson = active.listUploadsJSON(repoId)
                val items = PendingUpload.listFromJson(listJson)
                main.post {
                    uploadsAdapter.submit(items)
                    binding.textUploadsEmpty.visibility = if (items.isEmpty()) View.VISIBLE else View.GONE
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
        private const val PREF_ADDRESS = "address"
        private const val PREF_HOST_KEY = "host_public_key"
    }
}
