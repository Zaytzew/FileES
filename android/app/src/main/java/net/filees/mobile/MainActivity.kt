package net.filees.mobile

import android.Manifest
import android.animation.ObjectAnimator
import android.animation.ValueAnimator
import android.content.ActivityNotFoundException
import android.content.ContentValues
import android.content.Intent
import android.content.pm.PackageManager
import android.net.Uri
import android.widget.EditText
import android.os.Build
import android.os.Bundle
import android.os.Environment
import android.os.Handler
import android.os.Looper
import android.provider.MediaStore
import android.view.Menu
import android.view.MenuItem
import android.view.View
import android.view.animation.AccelerateDecelerateInterpolator
import android.webkit.MimeTypeMap
import androidx.activity.OnBackPressedCallback
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AlertDialog
import androidx.appcompat.app.AppCompatActivity
import androidx.core.content.ContextCompat
import androidx.core.content.FileProvider
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
    private var activeAddress: String? = null
    private var selectedRepoId: String? = null
    private var selectedShareName: String = ""
    private var selectableShares: List<RealmShare> = emptyList()
    private var manifestEntries: List<ManifestEntry> = emptyList()
    private var browsePrefix: String = ""
    private val browseAdapter = BrowseAdapter(onOpen = { openRow(it) }, onDownload = { downloadRow(it) })
    private val pendingAdapter = PendingUploadsAdapter(
        onDiscard = { discardPending(it) },
        onRetry = { retryPending(it) },
    )

    private val prefs by lazy { getSharedPreferences(FileesSession.PREFS, MODE_PRIVATE) }
    private var pulseAnimator: ObjectAnimator? = null

    private val pickFilesLauncher = registerForActivityResult(ActivityResultContracts.OpenMultipleDocuments()) { uris ->
        enqueueWalked(uris.map { DocumentWalk.single(contentResolver, it) })
    }
    private val pickFolderLauncher = registerForActivityResult(ActivityResultContracts.OpenDocumentTree()) { uri ->
        if (uri != null) enqueueFolder(uri)
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
        installFileesWindow()
        binding = ActivityMainBinding.inflate(layoutInflater)
        setContentView(binding.root)
        setSupportActionBar(binding.toolbar)
        binding.toolbar.padTopSystemBars()
        binding.buttonAdd.marginBottomSystemBars(16)
        binding.recyclerBrowse.padBottomSystemBars(88)
        watched = WatchedFolders(this)

        binding.recyclerBrowse.layoutManager = LinearLayoutManager(this)
        binding.recyclerBrowse.adapter = browseAdapter
        binding.recyclerPending.layoutManager = LinearLayoutManager(this)
        binding.recyclerPending.adapter = pendingAdapter
        binding.buttonScanQr.setOnClickListener { onScanQrClicked() }
        binding.buttonAdd.setOnClickListener { showAddChooser() }
        binding.barServerAddress.setOnClickListener { pickServer() }

        onBackPressedDispatcher.addCallback(this, object : OnBackPressedCallback(true) {
            override fun handleOnBackPressed() {
                if (!goUp()) finish()
            }
        })

        startPulse()

        FileesSession.migrate(prefs)
        selectedRepoId = FileesSession.current(prefs)?.selectedRepoId?.ifBlank { null }
        showPaired(false)
    }

    override fun onResume() {
        super.onResume()
        pulseAnimator?.resume()
        bindServerLabel()
        val address = prefs.getString(FileesSession.PREF_ADDRESS, null)
        val hostKey = prefs.getString(FileesSession.PREF_HOST_KEY, null)
        if (address != activeAddress) {
            client = null
        }
        if (!address.isNullOrBlank() && !hostKey.isNullOrBlank()) {
            if (client == null) {
                selectedRepoId = FileesSession.current(prefs)?.selectedRepoId?.ifBlank { null }
                activate(address, hostKey)
            } else {
                // activate() already ran successfully in an earlier
                // onResume() this process; showPaired(true) fired then, but
                // nothing re-asserts it on a later resume (e.g. back from
                // Settings) since scanWatchedFolders() only drives the
                // background upload watch, never the paired UI state.
                showPaired(true)
                scanWatchedFolders()
                refreshDecisions()
            }
        } else {
            showPaired(false)
        }
    }

    override fun onCreateOptionsMenu(menu: Menu): Boolean {
        menuInflater.inflate(R.menu.menu_main, menu)
        menu.findItem(R.id.action_settings)?.icon?.setTint(
            ContextCompat.getColor(this, R.color.filees_white),
        )
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

    // concepts/ANDROID_BRAND_ALIGNMENT_CONCEPT.md §5: purely decorative
    // "breathing" pulse on the brand dot next to the server address — same
    // 1.35s cadence as Wails's .pulse-core.is-offline animation, but this
    // one carries no meaning: Android has no connectivity state to show
    // (§0), so it never changes color or stops for anything other than the
    // activity going to background.
    private fun startPulse() {
        pulseAnimator = ObjectAnimator.ofFloat(binding.pulseDot, View.ALPHA, 1f, 0.35f).apply {
            duration = 1350
            repeatMode = ValueAnimator.REVERSE
            repeatCount = ValueAnimator.INFINITE
            interpolator = AccelerateDecelerateInterpolator()
            start()
        }
    }

    override fun onPause() {
        super.onPause()
        pulseAnimator?.pause()
    }

    override fun onDestroy() {
        super.onDestroy()
        pulseAnimator?.cancel()
    }

    private fun showPaired(paired: Boolean) {
        val hasServers = FileesSession.servers(prefs).isNotEmpty()
        binding.panelUnpaired.visibility = if (paired) View.GONE else View.VISIBLE
        binding.barServerAddress.visibility = if (paired || hasServers) View.VISIBLE else View.GONE
        binding.recyclerBrowse.visibility = if (paired) View.VISIBLE else View.GONE
        binding.buttonAdd.visibility = if (paired && canCaptureSelected()) View.VISIBLE else View.GONE
        if (!paired) bindDecisions(emptyList())
        supportActionBar?.setDisplayHomeAsUpEnabled(paired && selectedRepoId != null)
        bindServerLabel()
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
                FileesSession.putAndSelect(prefs, address, hostKey)
                main.post { activate(address, hostKey) }
            } catch (e: Exception) {
                main.post { failBusy(getString(R.string.error_pair), e) }
            }
        }
    }

    private fun activate(address: String, hostKey: String) {
        setBusy(true, getString(R.string.status_activating))
        io.execute {
            try {
                val dial = DialAddress.resolve(address)
                val newClient = Androidbind.newClient(filesDir.absolutePath, dial, FileesSession.MOBILE_USER, hostKey)
                main.post {
                    client = newClient
                    activeAddress = address
                    showPaired(true)
                    setBusy(false, "")
                    loadRealmProjection()
                    scanWatchedFolders()
                    refreshDecisions()
                }
            } catch (e: Exception) {
                main.post {
                    client = null
                    activeAddress = null
                    showPaired(false)
                    failBusy(getString(R.string.error_connect), e)
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
                    FileesSession.rememberProjection(prefs, projection)
                    bindServerLabel()
                    selectableShares = projection.shares.filter { it.selectable }
                    if (selectedRepoId != null && selectableShares.none { it.repoId == selectedRepoId }) {
                        selectedRepoId = null
                    }
                    renderList()
                    refreshDecisions()
                }
            } catch (e: Exception) {
                main.post {
                    if (looksRevoked(e)) showRevoked(e) else failBusy(getString(R.string.error_list), e)
                }
            }
        }
    }

    private fun renderList() {
        if (selectedRepoId == null) {
            selectedShareName = ""
            browsePrefix = ""
            browseAdapter.submit(shareRows(selectableShares))
            binding.toolbar.title = null
            binding.toolbar.subtitle = null
            binding.brandLockup.visibility = View.VISIBLE
            binding.buttonAdd.visibility = View.GONE
            supportActionBar?.setDisplayHomeAsUpEnabled(false)
            return
        }
        binding.brandLockup.visibility = View.GONE
        binding.buttonAdd.visibility = if (canCaptureSelected()) View.VISIBLE else View.GONE
        supportActionBar?.setDisplayHomeAsUpEnabled(true)
        binding.toolbar.title = if (browsePrefix.isEmpty()) selectedShareName else browsePrefix.substringAfterLast('/')
        val rows = ManifestBrowse.children(manifestEntries, browsePrefix)
        browseAdapter.submit(rows)
    }

    // Groups the top-level share list by purpose (ordinary repositories,
    // upload shelves, quarantine) instead of listing every kind mixed
    // together - the desktop projection already makes this distinction
    // (concepts/WAILS_UI_CLEANUP_CONCEPT.md §E2). A section header is only
    // inserted when more than one group is actually present, so a realm
    // with no upload shelves at all renders exactly as before.
    private fun shareRows(shares: List<RealmShare>): List<BrowseRow> {
        val groups = shares.groupBy { it.purpose }
        if (groups.size <= 1) {
            return shares.map { share -> shareRow(share) }
        }
        val order = listOf("", "upload_shelf", "upload_trash")
        val rows = mutableListOf<BrowseRow>()
        for (purpose in order + (groups.keys - order.toSet())) {
            val members = groups[purpose] ?: continue
            rows.add(BrowseRow("", "", directory = false, size = 0, sectionHeader = sectionLabel(purpose)))
            members.forEach { rows.add(shareRow(it)) }
        }
        return rows
    }

    private fun shareRow(share: RealmShare): BrowseRow =
        BrowseRow(share.displayName, "", directory = true, size = 0, repoId = share.repoId, share = true)

    private fun sectionLabel(purpose: String): String = when (purpose) {
        "upload_shelf" -> getString(R.string.section_upload_shelves)
        "upload_trash" -> getString(R.string.section_upload_trash)
        "" -> getString(R.string.section_repositories)
        else -> purpose
    }

    private fun openRow(row: BrowseRow) {
        if (row.share) {
            selectedRepoId = row.repoId
            selectedShareName = row.name
            FileesSession.setSelectedRepo(prefs, row.repoId)
            browsePrefix = ""
            refreshManifest()
            refreshDecisions()
            return
        }
        if (row.directory) {
            browsePrefix = row.path
            renderList()
            return
        }
        previewRow(row)
    }

    private fun goUp(): Boolean {
        if (selectedRepoId == null) return false
        if (browsePrefix.isNotEmpty()) {
            browsePrefix = browsePrefix.substringBeforeLast('/', "")
            renderList()
            return true
        }
        selectedRepoId = null
        FileesSession.setSelectedRepo(prefs, null)
        renderList()
        return true
    }

    private fun refreshManifest() {
        val active = client ?: return
        val repoId = selectedRepoId ?: return
        io.execute {
            try {
                val json = active.refreshJSON(repoId)
                val shouts = ManifestBrowse.shoutsFrom(json)
                main.post {
                    manifestEntries = ManifestBrowse.entriesFrom(json)
                    renderList()
                    showNewShouts(repoId, shouts)
                }
            } catch (e: Exception) {
                main.post { failBusy(getString(R.string.error_refresh), e) }
            }
        }
    }

    private fun bindServerLabel() {
        binding.textServerAddress.text = FileesSession.barLabel(prefs)
    }

    private fun selectedShare(): RealmShare? =
        selectableShares.firstOrNull { it.repoId == selectedRepoId }

    // Capture (Dodaj / watched folders) writes mobile-uploads/. Trash is
    // a reject waiting room: still listed and browsable, never a dump target.
    private fun canCaptureSelected(): Boolean = selectedShare()?.canCapture == true

    private fun showAddChooser() {
        if (!canCaptureSelected()) return
        AlertDialog.Builder(this)
            .setItems(arrayOf(getString(R.string.action_add_files), getString(R.string.action_add_folder))) { _, which ->
                if (which == 0) pickFilesLauncher.launch(arrayOf("*/*"))
                else pickFolderLauncher.launch(null)
            }
            .show()
    }

    private fun enqueueFolder(treeUri: Uri) {
        if (client == null || selectedRepoId.isNullOrBlank() || !canCaptureSelected()) return
        setBusy(true, getString(R.string.status_scanning))
        io.execute {
            val files = try {
                DocumentWalk.tree(contentResolver, treeUri)
            } catch (e: Exception) {
                main.post { failBusy(getString(R.string.error_send), e) }
                return@execute
            }
            if (files.isEmpty()) {
                main.post { setBusy(false, getString(R.string.browse_empty)) }
                return@execute
            }
            val summary = FolderPreflight.of(files)
            main.post { setBusy(true, preflightLabel(summary)) }
            if (summary.pack) {
                sendPacked(files)
            } else {
                sendOneByOne(files)
            }
        }
    }

    private fun preflightLabel(summary: FolderPreflight.Summary): String {
        val weight = HumanSize.format(summary.bytes)
        return resources.getQuantityString(R.plurals.status_preflight, summary.files, summary.files, weight)
    }

    private fun enqueueWalked(files: List<WalkedFile>) {
        if (client == null || selectedRepoId.isNullOrBlank() || files.isEmpty() || !canCaptureSelected()) return
        val summary = FolderPreflight.of(files)
        setBusy(true, preflightLabel(summary))
        io.execute {
            try {
                if (summary.pack) sendPacked(files) else sendOneByOne(files)
            } catch (e: Exception) {
                main.post { failBusy(getString(R.string.error_send), e) }
            }
        }
    }

    private fun sendPacked(files: List<WalkedFile>) {
        val active = client ?: return
        val repoId = selectedRepoId ?: return
        main.post { setBusy(true, getString(R.string.status_packing)) }
        var zip: File? = null
        try {
            zip = TreeZip.pack(contentResolver, files, cacheDir)
            main.post {
                setBusy(true, getString(R.string.status_sending_pack, HumanSize.format(zip.length())))
            }
            active.uploadTreeFile(repoId, UploadPaths.ROOT, files.size.toLong(), zip.absolutePath)
            main.post {
                setBusy(false, getString(R.string.status_sent_count, files.size))
                refreshManifest()
                refreshDecisions()
            }
        } catch (e: Exception) {
            main.post { failBusy(getString(R.string.error_tree), e) }
        } finally {
            zip?.delete()
        }
    }

    private fun sendOneByOne(files: List<WalkedFile>) {
        val active = client ?: return
        val repoId = selectedRepoId ?: return
        var done = 0
        try {
            for ((index, file) in files.withIndex()) {
                main.post {
                    setBusy(true, getString(R.string.status_sending_progress, index + 1, files.size))
                }
                val bytes = contentResolver.openInputStream(file.uri)?.use { it.readBytes() } ?: continue
                active.enqueueUpload(repoId, UploadPaths.parent(file.relativeDir), file.filename, file.contentType, bytes)
                val report = UploadDrain.run(active, repoId)
                if (report.transportError != null) {
                    throw RuntimeException(report.transportError)
                }
                done++
            }
            main.post {
                setBusy(false, getString(R.string.status_sent_count, done))
                refreshManifest()
                refreshDecisions()
            }
        } catch (e: Exception) {
            val sent = done
            main.post {
                failBusy(getString(R.string.error_send_partial, sent, files.size), e)
                refreshManifest()
                refreshDecisions()
            }
        }
    }

    private fun previewRow(row: BrowseRow) {
        val active = client ?: return
        val repoId = selectedRepoId ?: return
        setBusy(true, getString(R.string.status_downloading))
        io.execute {
            try {
                val dest = cachedDownload(active, repoId, row)
                main.post {
                    setBusy(false, "")
                    openCached(dest, row.name)
                }
            } catch (e: Exception) {
                main.post { failBusy(getString(R.string.error_download), e) }
            }
        }
    }

    private fun downloadRow(row: BrowseRow) {
        if (row.directory) {
            downloadFolder(row)
            return
        }
        val active = client ?: return
        val repoId = selectedRepoId ?: return
        setBusy(true, getString(R.string.status_downloading))
        io.execute {
            try {
                val dest = cachedDownload(active, repoId, row)
                publishDownload(dest, row.name)
                main.post { setBusy(false, getString(R.string.status_downloaded, row.name)) }
            } catch (e: Exception) {
                main.post { failBusy(getString(R.string.error_download), e) }
            }
        }
    }

    private fun downloadFolder(row: BrowseRow) {
        val files = ManifestBrowse.filesUnder(manifestEntries, row.path)
        if (files.isEmpty()) {
            setBusy(false, getString(R.string.browse_empty))
            return
        }
        val bytes = files.sumOf { it.size }
        if (files.size > 200 || bytes > 400L * 1024 * 1024) {
            AlertDialog.Builder(this)
                .setMessage(getString(R.string.error_folder_too_big, HumanSize.format(bytes)))
                .setPositiveButton(android.R.string.ok, null)
                .show()
            return
        }
        val start = {
            pullFolderZip(row, files)
        }
        if (files.size >= 40 || bytes >= 40L * 1024 * 1024) {
            val summary = resources.getQuantityString(
                R.plurals.status_preflight, files.size, files.size, HumanSize.format(bytes),
            )
            AlertDialog.Builder(this)
                .setTitle(R.string.download_folder_confirm_title)
                .setMessage(getString(R.string.download_folder_confirm, row.name, summary))
                .setPositiveButton(R.string.action_download) { _, _ -> start() }
                .setNegativeButton(R.string.action_cancel, null)
                .show()
            return
        }
        start()
    }

    private fun pullFolderZip(row: BrowseRow, files: List<ManifestEntry>) {
        val active = client ?: return
        val repoId = selectedRepoId ?: return
        setBusy(true, getString(R.string.status_downloading_folder, 1, files.size))
        io.execute {
            val staged = ArrayList<Pair<String, File>>(files.size)
            var zip: File? = null
            try {
                val dir = File(cacheDir, "dl").apply { mkdirs() }
                val pfx = if (row.path.isEmpty()) "" else "${row.path}/"
                for ((index, entry) in files.withIndex()) {
                    main.post {
                        setBusy(true, getString(R.string.status_downloading_folder, index + 1, files.size))
                    }
                    val relative = if (pfx.isEmpty()) entry.path else entry.path.removePrefix(pfx)
                    val dest = File(dir, "part-${index}-${relative.substringAfterLast('/')}")
                    active.downloadTo(repoId, entry.path, dest.absolutePath)
                    staged.add(relative to dest)
                }
                main.post { setBusy(true, getString(R.string.status_packing_download)) }
                zip = TreeZip.packNamed(staged, dir, "${row.name}.zip")
                publishDownload(zip, "${row.name}.zip")
                main.post { setBusy(false, getString(R.string.status_downloaded, "${row.name}.zip")) }
            } catch (e: Exception) {
                main.post { failBusy(getString(R.string.error_download), e) }
            } finally {
                staged.forEach { it.second.delete() }
                zip?.delete()
            }
        }
    }

    private fun showNewShouts(repoId: String, shouts: List<Pair<Long, String>>) {
        val fresh = shouts.filter {
            !FileesSession.isShoutAcked(prefs, FileesSession.shoutId(repoId, it.first))
        }
        if (fresh.isEmpty()) return
        AlertDialog.Builder(this)
            .setTitle(R.string.shouts_title)
            .setMessage(fresh.joinToString("\n\n") { it.second })
            .setPositiveButton(R.string.shouts_ack) { _, _ ->
                FileesSession.ackShouts(prefs, fresh.map { FileesSession.shoutId(repoId, it.first) })
            }
            .show()
    }

    private fun cachedDownload(active: Client, repoId: String, row: BrowseRow): File {
        val dir = File(cacheDir, "dl").apply { mkdirs() }
        val dest = File(dir, row.name)
        active.downloadTo(repoId, row.path, dest.absolutePath)
        return dest
    }

    private fun openCached(file: File, name: String) {
        val mime = MimeTypeMap.getSingleton().getMimeTypeFromExtension(file.extension.lowercase())
            ?: "application/octet-stream"
        val uri = FileProvider.getUriForFile(this, "$packageName.files", file)
        val intent = Intent(Intent.ACTION_VIEW)
            .setDataAndType(uri, mime)
            .addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
        try {
            startActivity(intent)
        } catch (_: ActivityNotFoundException) {
            publishDownload(file, name)
            binding.toolbar.subtitle = getString(R.string.error_preview)
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

    private fun uploadRepoId(): String? =
        selectedRepoId?.takeIf { canCaptureSelected() }
            ?: prefs.getString(FileesSession.PREF_UPLOAD_REPO_ID, null)

    private fun refreshDecisions() {
        val active = client ?: run {
            bindDecisions(emptyList())
            return
        }
        val repoId = uploadRepoId() ?: run {
            bindDecisions(emptyList())
            return
        }
        io.execute {
            try {
                val items = PendingUpload.listFromJson(active.listUploadsJSON(repoId)).filter { it.needsDecision }
                main.post { bindDecisions(items) }
            } catch (_: Exception) {
                main.post { bindDecisions(emptyList()) }
            }
        }
    }

    private fun bindDecisions(items: List<PendingUpload>) {
        binding.panelDecisions.visibility = if (items.isEmpty()) View.GONE else View.VISIBLE
        pendingAdapter.submit(items)
        if (items.isNotEmpty()) {
            binding.textDecisionsHeader.text = getString(R.string.status_waiting_decision, items.size)
        }
    }

    private fun discardPending(item: PendingUpload) {
        val active = client ?: return
        val repoId = uploadRepoId() ?: return
        io.execute {
            try {
                active.discardUpload(repoId, item.id)
                main.post { refreshDecisions() }
            } catch (e: Exception) {
                main.post { failBusy(getString(R.string.error_send), e) }
            }
        }
    }

    private fun retryPending(item: PendingUpload) {
        if (item.state == "conflict") {
            val input = EditText(this)
            input.setText(item.filename)
            AlertDialog.Builder(this)
                .setTitle(R.string.action_send_as)
                .setView(input)
                .setPositiveButton(android.R.string.ok) { _, _ ->
                    spoolRetry(item, input.text.toString().trim())
                }
                .setNegativeButton(R.string.action_cancel, null)
                .show()
            return
        }
        spoolRetry(item, item.filename)
    }

    private fun spoolRetry(item: PendingUpload, filename: String) {
        val active = client ?: return
        val repoId = uploadRepoId() ?: return
        if (filename.isBlank()) return
        io.execute {
            try {
                val payload = File(filesDir, "uploads/$repoId/${item.id}.bin").readBytes()
                val type = item.contentType.ifBlank { "application/octet-stream" }
                active.enqueueUpload(repoId, item.parentPath, filename, type, payload)
                active.discardUpload(repoId, item.id)
                val report = UploadDrain.run(active, repoId)
                if (report.transportError != null) {
                    throw RuntimeException(report.transportError)
                }
                main.post {
                    refreshDecisions()
                    refreshManifest()
                }
            } catch (e: Exception) {
                main.post { failBusy(getString(R.string.error_send), e) }
            }
        }
    }

    private fun looksRevoked(err: Exception): Boolean {
        val text = (err.message ?: "").lowercase()
        return "access denied" in text || "access.denied" in text || "revoked" in text
    }

    private fun showRevoked(err: Exception) {
        setBusy(false, "")
        AlertDialog.Builder(this)
            .setTitle(R.string.error_connect)
            .setMessage(getString(R.string.error_revoked) + "\n\n" + (err.message ?: ""))
            .setPositiveButton(R.string.action_unpair) { _, _ -> unpairNow() }
            .setNegativeButton(R.string.action_cancel, null)
            .show()
    }

    private fun pickServer() {
        val all = FileesSession.servers(prefs)
        val currentId = FileesSession.current(prefs)?.id
        val labels = all.map { server ->
            if (server.id == currentId) "✓ ${server.label()}" else server.label()
        } + getString(R.string.action_add_server)
        AlertDialog.Builder(this)
            .setTitle(R.string.action_switch_server)
            .setItems(labels.toTypedArray()) { _, index ->
                if (index >= all.size) {
                    onScanQrClicked()
                    return@setItems
                }
                switchTo(all[index])
            }
            .show()
    }

    private fun switchTo(server: PairedServer) {
        if (FileesSession.current(prefs)?.id == server.id && client != null) return
        FileesSession.select(prefs, server.id)
        client = null
        selectedRepoId = FileesSession.current(prefs)?.selectedRepoId?.ifBlank { null }
        selectedShareName = ""
        browsePrefix = ""
        selectableShares = emptyList()
        manifestEntries = emptyList()
        bindDecisions(emptyList())
        bindServerLabel()
        activate(server.address, server.hostKey)
    }

    private fun unpairNow() {
        FileesSession.unpair(prefs)
        client = null
        activeAddress = null
        selectedRepoId = null
        selectableShares = emptyList()
        manifestEntries = emptyList()
        bindDecisions(emptyList())
        bindServerLabel()
        val next = FileesSession.current(prefs)
        if (next != null) {
            selectedRepoId = next.selectedRepoId.ifBlank { null }
            activate(next.address, next.hostKey)
        } else {
            showPaired(false)
        }
    }

    private fun scanWatchedFolders() {
        FileesWatchScheduler.ensure(this)
        if (client == null || selectedRepoId.isNullOrBlank() || watched.uris().isEmpty()) return
        io.execute {
            try {
                FileesWatchTick.run(this)
            } catch (_: Exception) {
            }
            main.post { refreshDecisions() }
        }
    }

    private fun failBusy(headline: String, err: Exception) {
        setBusy(false, headline)
        showTransportError(headline, err, prefs.getString(FileesSession.PREF_ADDRESS, null))
    }

    private fun setBusy(busy: Boolean, message: String) {
        binding.overlayBusy.visibility = if (busy) View.VISIBLE else View.GONE
        if (message.isNotBlank()) binding.textBusy.text = message
        // toolbar.subtitle occupies the same Toolbar-managed slot as
        // brandLockup (§4's wordmark ImageView) - Toolbar lays out its own
        // title/subtitle text independently of arbitrary child views, the
        // same class of clash app:logo had with app:title. Every current
        // caller only reaches this with a non-empty message from inside a
        // repository (brandLockup already GONE there), but gate on the
        // view directly instead of trusting that to stay true: an error
        // that happens to fire while still on the top-level share list
        // (confirmed live - "Nie udało się odświeżyć" overlapping the
        // wordmark) must not resurrect the subtitle there.
        if (!busy && binding.brandLockup.visibility != View.VISIBLE) {
            binding.toolbar.subtitle = message
        }
    }
}
