package net.filees.mobile

import android.Manifest
import android.content.ContentValues
import android.content.Intent
import android.content.pm.PackageManager
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.os.Environment
import android.os.Handler
import android.os.Looper
import android.provider.MediaStore
import android.view.Menu
import android.view.MenuItem
import android.view.View
import android.webkit.MimeTypeMap
import androidx.activity.OnBackPressedCallback
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AlertDialog
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
import java.io.File
import java.util.concurrent.Executors

class MainActivity : AppCompatActivity() {

    private lateinit var binding: ActivityMainBinding
    private lateinit var watched: WatchedFolders
    private val io = Executors.newSingleThreadExecutor()
    private val main = Handler(Looper.getMainLooper())

    private var client: Client? = null
    private var selectedRepoId: String? = null
    private var selectedShareName: String = ""
    private var selectableShares: List<RealmShare> = emptyList()
    private var manifestEntries: List<ManifestEntry> = emptyList()
    private var browsePrefix: String = ""
    private val browseAdapter = BrowseAdapter(onOpen = { openRow(it) }, onDownload = { downloadRow(it) })

    private val prefs by lazy { getSharedPreferences(FileesSession.PREFS, MODE_PRIVATE) }

    private val pickFilesLauncher = registerForActivityResult(ActivityResultContracts.OpenMultipleDocuments()) { uris ->
        enqueueWalked(uris.map { DocumentWalk.single(contentResolver, it) })
    }
    private val pickFolderLauncher = registerForActivityResult(ActivityResultContracts.OpenDocumentTree()) { uri ->
        if (uri != null) enqueueWalked(DocumentWalk.tree(contentResolver, uri))
    }
    private val scanLauncher = registerForActivityResult(ScanContract()) { result ->
        if (result.contents == null) {
            setBusy(false, "")
        } else {
            pairFromPayload(result.contents)
        }
    }
    private val cameraPermissionLauncher = registerForActivityResult(ActivityResultContracts.RequestPermission()) { granted ->
        if (granted) launchScanner()
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        binding = ActivityMainBinding.inflate(layoutInflater)
        setContentView(binding.root)
        setSupportActionBar(binding.toolbar)
        watched = WatchedFolders(this)

        binding.recyclerBrowse.layoutManager = LinearLayoutManager(this)
        binding.recyclerBrowse.adapter = browseAdapter
        binding.buttonScanQr.setOnClickListener { onScanQrClicked() }
        binding.buttonAdd.setOnClickListener { showAddChooser() }

        onBackPressedDispatcher.addCallback(this, object : OnBackPressedCallback(true) {
            override fun handleOnBackPressed() {
                if (!goUp()) finish()
            }
        })

        selectedRepoId = prefs.getString(FileesSession.PREF_REPO_ID, null)
        showPaired(false)
    }

    override fun onResume() {
        super.onResume()
        val address = prefs.getString(FileesSession.PREF_ADDRESS, null)
        val hostKey = prefs.getString(FileesSession.PREF_HOST_KEY, null)
        if (!address.isNullOrBlank() && !hostKey.isNullOrBlank()) {
            if (client == null) activate(address, hostKey) else scanWatchedFolders()
        } else {
            showPaired(false)
        }
    }

    override fun onCreateOptionsMenu(menu: Menu): Boolean {
        menuInflater.inflate(R.menu.menu_main, menu)
        return true
    }

    override fun onOptionsItemSelected(item: MenuItem): Boolean {
        if (item.itemId == android.R.id.home) {
            if (!goUp()) finish()
            return true
        }
        if (item.itemId == R.id.action_settings) {
            startActivity(Intent(this, SettingsActivity::class.java))
            return true
        }
        return super.onOptionsItemSelected(item)
    }

    private fun showPaired(paired: Boolean) {
        binding.panelUnpaired.visibility = if (paired) View.GONE else View.VISIBLE
        binding.recyclerBrowse.visibility = if (paired) View.VISIBLE else View.GONE
        binding.buttonAdd.visibility = if (paired && !selectedRepoId.isNullOrBlank()) View.VISIBLE else View.GONE
        supportActionBar?.setDisplayHomeAsUpEnabled(paired && selectedRepoId != null)
    }

    private fun onScanQrClicked() {
        if (ContextCompat.checkSelfPermission(this, Manifest.permission.CAMERA) == PackageManager.PERMISSION_GRANTED) {
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
        scanLauncher.launch(options)
    }

    private fun pairFromPayload(payload: String) {
        val json = try {
            JSONObject(payload)
        } catch (_: Exception) {
            return
        }
        setBusy(true, getString(R.string.status_pairing))
        io.execute {
            try {
                val address = json.getString("address")
                val hostKey = json.getString("host_public_key")
                Androidbind.pairJSON(filesDir.absolutePath, address, hostKey, json.getString("token"))
                prefs.edit {
                    putString(FileesSession.PREF_ADDRESS, address)
                    putString(FileesSession.PREF_HOST_KEY, hostKey)
                }
                main.post { activate(address, hostKey) }
            } catch (e: Exception) {
                main.post { setBusy(false, e.message ?: e.toString()) }
            }
        }
    }

    private fun activate(address: String, hostKey: String) {
        setBusy(true, getString(R.string.status_activating))
        io.execute {
            try {
                val newClient = Androidbind.newClient(filesDir.absolutePath, address, FileesSession.MOBILE_USER, hostKey)
                main.post {
                    client = newClient
                    showPaired(true)
                    setBusy(false, "")
                    loadRealmProjection()
                    scanWatchedFolders()
                }
            } catch (e: Exception) {
                main.post {
                    showPaired(false)
                    setBusy(false, e.message ?: e.toString())
                }
            }
        }
    }

    private fun loadRealmProjection() {
        val active = client ?: return
        io.execute {
            try {
                val projection = RealmProjection.fromJson(active.listRepositoriesJSON())
                main.post {
                    selectableShares = projection.shares.filter { it.selectable }
                    if (selectedRepoId != null && selectableShares.none { it.repoId == selectedRepoId }) {
                        selectedRepoId = null
                    }
                    renderList()
                }
            } catch (e: Exception) {
                main.post { setBusy(false, e.message ?: e.toString()) }
            }
        }
    }

    private fun renderList() {
        if (selectedRepoId == null) {
            selectedShareName = ""
            browsePrefix = ""
            browseAdapter.submit(
                selectableShares.map { share ->
                    BrowseRow(share.displayName, "", directory = true, size = 0, repoId = share.repoId, share = true)
                },
            )
            binding.toolbar.title = getString(R.string.app_name)
            binding.buttonAdd.visibility = View.GONE
            supportActionBar?.setDisplayHomeAsUpEnabled(false)
            return
        }
        binding.buttonAdd.visibility = View.VISIBLE
        supportActionBar?.setDisplayHomeAsUpEnabled(true)
        binding.toolbar.title = if (browsePrefix.isEmpty()) selectedShareName else browsePrefix.substringAfterLast('/')
        val rows = ManifestBrowse.children(manifestEntries, browsePrefix)
        browseAdapter.submit(rows)
    }

    private fun openRow(row: BrowseRow) {
        if (row.share) {
            selectedRepoId = row.repoId
            selectedShareName = row.name
            prefs.edit { putString(FileesSession.PREF_REPO_ID, row.repoId) }
            browsePrefix = ""
            refreshManifest()
            return
        }
        if (row.directory) {
            browsePrefix = row.path
            renderList()
        }
    }

    private fun goUp(): Boolean {
        if (selectedRepoId == null) return false
        if (browsePrefix.isNotEmpty()) {
            browsePrefix = browsePrefix.substringBeforeLast('/', "")
            renderList()
            return true
        }
        selectedRepoId = null
        prefs.edit { remove(FileesSession.PREF_REPO_ID) }
        renderList()
        return true
    }

    private fun refreshManifest() {
        val active = client ?: return
        val repoId = selectedRepoId ?: return
        io.execute {
            try {
                val json = active.refreshJSON(repoId)
                main.post {
                    manifestEntries = ManifestBrowse.entriesFrom(json)
                    renderList()
                }
            } catch (e: Exception) {
                main.post { setBusy(false, e.message ?: e.toString()) }
            }
        }
    }

    private fun showAddChooser() {
        AlertDialog.Builder(this)
            .setItems(arrayOf(getString(R.string.action_add_files), getString(R.string.action_add_folder))) { _, which ->
                if (which == 0) pickFilesLauncher.launch(arrayOf("*/*"))
                else pickFolderLauncher.launch(null)
            }
            .show()
    }

    private fun enqueueWalked(files: List<WalkedFile>) {
        val active = client
        val repoId = selectedRepoId
        if (active == null || repoId.isNullOrBlank() || files.isEmpty()) return
        setBusy(true, getString(R.string.status_sending))
        io.execute {
            try {
                for (file in files) {
                    val bytes = contentResolver.openInputStream(file.uri)?.use { it.readBytes() } ?: continue
                    active.enqueueUpload(repoId, UploadPaths.parent(file.relativeDir), file.filename, file.contentType, bytes)
                }
                active.drainPendingJSON(repoId)
                main.post {
                    setBusy(false, getString(R.string.status_sent))
                    refreshManifest()
                }
            } catch (e: Exception) {
                main.post { setBusy(false, e.message ?: e.toString()) }
            }
        }
    }

    private fun downloadRow(row: BrowseRow) {
        val active = client ?: return
        val repoId = selectedRepoId ?: return
        setBusy(true, getString(R.string.status_downloading))
        io.execute {
            try {
                val dir = File(cacheDir, "dl").apply { mkdirs() }
                val dest = File(dir, row.name)
                active.downloadTo(repoId, row.path, dest.absolutePath)
                publishDownload(dest, row.name)
                main.post { setBusy(false, getString(R.string.status_downloaded, row.name)) }
            } catch (e: Exception) {
                main.post { setBusy(false, e.message ?: e.toString()) }
            }
        }
    }

    private fun publishDownload(file: File, name: String) {
        val mime = MimeTypeMap.getSingleton().getMimeTypeFromExtension(file.extension.lowercase()) ?: "application/octet-stream"
        if (Build.VERSION.SDK_INT >= 29) {
            val values = ContentValues().apply {
                put(MediaStore.Downloads.DISPLAY_NAME, name)
                put(MediaStore.Downloads.MIME_TYPE, mime)
                put(MediaStore.Downloads.IS_PENDING, 1)
            }
            val uri = contentResolver.insert(MediaStore.Downloads.EXTERNAL_CONTENT_URI, values) ?: return
            contentResolver.openOutputStream(uri)?.use { out -> file.inputStream().use { it.copyTo(out) } }
            values.clear()
            values.put(MediaStore.Downloads.IS_PENDING, 0)
            contentResolver.update(uri, values, null, null)
            return
        }
        val publicDir = Environment.getExternalStoragePublicDirectory(Environment.DIRECTORY_DOWNLOADS)
        publicDir.mkdirs()
        file.copyTo(File(publicDir, name), overwrite = true)
    }

    private fun scanWatchedFolders() {
        FileesWatchScheduler.ensure(this)
        if (client == null || selectedRepoId.isNullOrBlank() || watched.uris().isEmpty()) return
        io.execute {
            try {
                FileesWatchTick.run(this)
            } catch (_: Exception) {
            }
        }
    }

    private fun setBusy(busy: Boolean, message: String) {
        binding.overlayBusy.visibility = if (busy) View.VISIBLE else View.GONE
        if (message.isNotBlank()) binding.textBusy.text = message
        if (!busy && message.isNotBlank()) {
            binding.toolbar.subtitle = message
        }
    }
}
