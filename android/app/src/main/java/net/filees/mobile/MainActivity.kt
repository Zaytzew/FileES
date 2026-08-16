package net.filees.mobile

import android.Manifest
import android.content.pm.PackageManager
import android.net.Uri
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.provider.OpenableColumns
import android.view.View
import android.widget.ArrayAdapter
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
    private var selectedRepoId: String? = null
    private var selectableShares: List<RealmShare> = emptyList()
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
        binding.pickerShare.setOnItemClickListener { _, _, position, _ ->
            if (position in selectableShares.indices) {
                selectShare(selectableShares[position])
            }
        }
        selectedRepoId = prefs.getString(PREF_REPO_ID, null)

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
        options.setCaptureActivity(QrCaptureActivity::class.java)
        options.setDesiredBarcodeFormats(ScanOptions.QR_CODE)
        options.setPrompt(getString(R.string.scan_qr_prompt))
        options.setBeepEnabled(false)
        options.setOrientationLocked(true)
        options.addExtra("TRY_HARDER", true)
        options.addExtra("CHARACTER_SET", "UTF-8")
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
            binding.editPairingPayload.text?.clear()
            setStatus(getString(R.string.status_error, "nieprawidłowe dane parowania"))
            return
        }
        // The token must not linger in the UI a moment longer than parsing
        // needs it - clear synchronously here, not after pairAndActivate's
        // async network round trip completes.
        binding.editPairingPayload.text?.clear()
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
                    loadRealmProjection()
                }
            } catch (e: Exception) {
                main.post { setStatus(getString(R.string.status_error, e.message ?: e.toString())) }
            }
        }
    }

    private fun onRefreshClicked() {
        val active = client
        if (active == null) {
            setStatus(getString(R.string.status_error, "aktywuj klienta najpierw"))
            return
        }
        val repoId = currentRepoId()
        setStatus(getString(R.string.status_refreshing))
        io.execute {
            try {
                val projectionJson = active.listRepositoriesJSON()
                val manifestJson = if (repoId.isNotEmpty()) active.refreshJSON(repoId) else null
                main.post {
                    applyProjection(projectionJson)
                    if (manifestJson != null) {
                        binding.textManifest.text = formatManifest(manifestJson)
                    }
                    setStatus(getString(R.string.status_activated, pairedAddress ?: ""))
                    refreshUploadsList()
                }
            } catch (e: Exception) {
                main.post { setStatus(getString(R.string.status_error, e.message ?: e.toString())) }
            }
        }
    }

    private fun onFilePicked(uri: Uri) {
        val active = client
        val repoId = currentRepoId()
        if (active == null) {
            setStatus(getString(R.string.status_error, "aktywuj klienta najpierw"))
            return
        }
        if (repoId.isEmpty()) {
            setStatus(getString(R.string.status_error, "wybierz udział strefy"))
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
        val repoId = currentRepoId()
        if (active == null) {
            setStatus(getString(R.string.status_error, "aktywuj klienta najpierw"))
            return
        }
        if (repoId.isEmpty()) {
            setStatus(getString(R.string.status_error, "wybierz udział strefy"))
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
        val repoId = currentRepoId()
        if (repoId.isEmpty()) return
        io.execute {
            try {
                active.discardUpload(repoId, item.id)
                main.post { refreshUploadsList() }
            } catch (e: Exception) {
                main.post { setStatus(getString(R.string.status_error, e.message ?: e.toString())) }
            }
        }
    }

    private fun loadRealmProjection() {
        val active = client ?: return
        io.execute {
            try {
                val json = active.listRepositoriesJSON()
                main.post {
                    applyProjection(json)
                    refreshUploadsList()
                }
            } catch (e: Exception) {
                main.post {
                    binding.textRealmProjection.text = ""
                    setStatus(getString(R.string.status_error, e.message ?: e.toString()))
                }
            }
        }
    }

    private fun applyProjection(json: String) {
        val projection = RealmProjection.fromJson(json)
        binding.textRealmProjection.text = formatProjection(projection)
        selectableShares = projection.shares.filter { it.selectable }
        val labels = selectableShares.map { share ->
            if (share.access == "rw") {
                getString(R.string.picker_share_rw, share.displayName)
            } else {
                getString(R.string.picker_share_r, share.displayName)
            }
        }
        binding.pickerShare.setAdapter(
            ArrayAdapter(this, android.R.layout.simple_dropdown_item_1line, labels)
        )
        val saved = selectedRepoId
        val restored = selectableShares.firstOrNull { it.repoId == saved }
            ?: selectableShares.singleOrNull()
        if (restored != null) {
            val index = selectableShares.indexOf(restored)
            binding.pickerShare.setText(labels[index], false)
            selectShare(restored)
        } else {
            selectedRepoId = null
            binding.pickerShare.setText("", false)
            prefs.edit { remove(PREF_REPO_ID) }
            uploadsAdapter.submit(emptyList())
            binding.textUploadsEmpty.visibility = View.VISIBLE
        }
    }

    private fun selectShare(share: RealmShare) {
        selectedRepoId = share.repoId
        prefs.edit { putString(PREF_REPO_ID, share.repoId) }
        refreshUploadsList()
    }

    private fun currentRepoId(): String = selectedRepoId.orEmpty()

    private fun formatProjection(projection: RealmProjection): String {
        if (projection.shares.isEmpty()) {
            return getString(R.string.projection_empty)
        }
        val builder = StringBuilder()
        val realm = projection.realmAlias.ifBlank { projection.realmId }
        if (realm.isNotBlank()) {
            builder.append(getString(R.string.projection_realm, realm)).append('\n')
        }
        for (share in projection.shares) {
            val line = if (share.access == "rw") {
                getString(R.string.projection_share_rw, share.displayName)
            } else {
                getString(R.string.projection_share_r, share.displayName)
            }
            builder.append("• ").append(line).append('\n')
        }
        return builder.toString().trimEnd()
    }

    private fun refreshUploadsList() {
        val active = client ?: return
        val repoId = currentRepoId()
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
        private const val PREF_REPO_ID = "selected_repo_id"
    }
}

private data class RealmShare(
    val repoId: String,
    val displayName: String,
    val access: String,
    val state: String,
) {
    val selectable: Boolean
        get() = (state == "active" || state == "initializing") && (access == "r" || access == "rw")
}

private data class RealmProjection(
    val realmId: String,
    val realmAlias: String,
    val shares: List<RealmShare>,
) {
    companion object {
        fun fromJson(json: String): RealmProjection {
            if (json.isBlank()) {
                return RealmProjection("", "", emptyList())
            }
            val root = JSONObject(json)
            val entries = root.optJSONArray("repositories")
            val shares = mutableListOf<RealmShare>()
            if (entries != null) {
                for (i in 0 until entries.length()) {
                    val item = entries.getJSONObject(i)
                    shares.add(
                        RealmShare(
                            repoId = item.optString("repo_id"),
                            displayName = item.optString("display_name"),
                            access = item.optString("access"),
                            state = item.optString("state"),
                        )
                    )
                }
            }
            return RealmProjection(
                realmId = root.optString("realm_id"),
                realmAlias = root.optString("realm_alias"),
                shares = shares,
            )
        }
    }
}
