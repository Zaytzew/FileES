package net.filees.mobile

import android.Manifest
import android.animation.ObjectAnimator
import android.animation.ValueAnimator
import android.content.ActivityNotFoundException
import android.content.ContentValues
import android.content.Context
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
import java.io.InterruptedIOException
import java.util.concurrent.Executors

// Newest journal entries shown before "show all"; the cap itself is FileesSession.JOURNAL_CAP.
private const val JOURNAL_COLLAPSED = 3

class MainActivity : AppCompatActivity() {
    private val prefsCollapsed = "collapsed_drawers"

    private lateinit var binding: ActivityMainBinding
    private lateinit var watched: WatchedFolders
    private val io = Executors.newSingleThreadExecutor()
    private val main = Handler(Looper.getMainLooper())
    private val uiRequests = UiRequests(::sessionKey)
    private class ShareTask(var ticket: UiRequests.Ticket, val limitBytes: Long) {
        @Volatile var cancelled = false
            private set
        @Volatile var client: Client? = null
            private set
        fun attach(client: Client) {
            this.client = client
            if (cancelled) client.cancel()
            check()
        }
        fun cancel() {
            cancelled = true
            close()
        }
        fun close() { client?.cancel() }
        fun check() { if (cancelled) throw InterruptedIOException("Share cancelled") }
    }
    private class SharePackageTooLarge(val actual: Long, val limit: Long) : java.io.IOException("Share package limit exceeded")
    private var shareTask: ShareTask? = null
    private var shareDialog: AlertDialog? = null
    private var shareConfirmDialog: AlertDialog? = null
    private var pickerTarget: CaptureDestination<Client>? = null
    private var pendingDownloadPermission: Pair<UiRequests.Ticket, () -> Unit>? = null

    private val downloadPermission = registerForActivityResult(ActivityResultContracts.RequestPermission()) { granted ->
        val pending = pendingDownloadPermission
        pendingDownloadPermission = null
        if (pending != null && uiRequests.current(pending.first) && !isDestroyed && !isFinishing) {
            if (granted) pending.second()
            else requestBusy(pending.first, false, getString(R.string.error_download_permission))
        }
    }

    private var client: Client? = null
    private var activeSession: Triple<String?, String?, String?>? = null
    private var selectedRepoId: String? = null
    private var selectedShareName: String = ""
    private var selectableShares: List<RealmShare> = emptyList()
    private var drawerFrame: DrawerFrame = DrawerFrame.empty()
    private var manifestEntries: List<ManifestEntry> = emptyList()
    private var browsePrefix: String = ""
    private var browseRevision: Long = 0
    private var browseGeneration: Long = 0
    private var legacyFullTree: Boolean = false
    private var confirmedRepoId: String? = null
    private var confirmedRevision: Long = 0
    private var confirmedGeneration: Long = 0
    private var browseOffline: Boolean = false
    private var pendingDecisions: List<PendingUpload> = emptyList()
    private val browseAdapter = BrowseAdapter(
        onOpen = { openRow(it) }, onDownload = { downloadRow(it) }, onShare = { shareRow(it) },
    )
    private val pendingAdapter = PendingUploadsAdapter(
        onDiscard = { discardPending(it) },
        onRetry = { retryPending(it) },
    )

    private val prefs by lazy { getSharedPreferences(FileesSession.PREFS, MODE_PRIVATE) }
    private var pulseAnimator: ObjectAnimator? = null
    private var journalExpanded = false

    private val pickFilesLauncher = registerForActivityResult(ActivityResultContracts.OpenMultipleDocuments()) { uris ->
        val target = takePickerTarget()
        if (target != null) enqueueWalked(target, uris.map { DocumentWalk.single(contentResolver, it) })
    }
    private val pickFolderLauncher = registerForActivityResult(ActivityResultContracts.OpenDocumentTree()) { uri ->
        val target = takePickerTarget()
        if (uri != null && target != null) enqueueFolder(target, uri)
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

    private val watchStatus by lazy { WatchStatusStore(this) }
    private val watchListener = android.content.SharedPreferences.OnSharedPreferenceChangeListener { _, _ ->
        main.post { if (!isDestroyed) browseAdapter.updateCaptureSummary(watchStatus.summary(WatchStatusStore.scope(this),watched.uris())) }
    }

    override fun attachBaseContext(newBase: Context) {
        super.attachBaseContext(FileesLocale.wrap(newBase))
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
        watchStatus.prefs.registerOnSharedPreferenceChangeListener(watchListener)
        if (FileesLocale.mismatch(this)) {
            recreate()
            return
        }
        pulseAnimator?.resume()
        watched = WatchedFolders(this)
        bindServerLabel()
        val address = prefs.getString(FileesSession.PREF_ADDRESS, null)
        val hostKey = prefs.getString(FileesSession.PREF_HOST_KEY, null)
        if (sessionKey() != activeSession) {
            invalidateUiRequests()
            client = null
        }
        if (!address.isNullOrBlank() && !hostKey.isNullOrBlank()) {
            if (client == null) {
                selectedRepoId = FileesSession.current(prefs)?.selectedRepoId?.ifBlank { null }
                // The watch reads its own paired target. A DNS miss in the
                // browser must not postpone a file already waiting to send.
                scanWatchedFolders()
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

    // implementation notes (not distributed) §5: purely decorative
    // "breathing" pulse on the brand dot next to the server address — same
    // 1.35s cadence as Wails's .pulse-core.is-offline animation, but this
    // one carries no meaning: Android has no connectivity state to show
    // (§0), so it never changes color or stops for anything other than the
    // activity going to background.
    private fun startPulse() {
        pulseAnimator = ObjectAnimator.ofFloat(binding.pulseDot, View.ALPHA, 1f, 0.55f).apply {
            duration = 1350
            repeatMode = ValueAnimator.REVERSE
            repeatCount = ValueAnimator.INFINITE
            interpolator = AccelerateDecelerateInterpolator()
            start()
        }
    }

    override fun onPause() {
        watchStatus.prefs.unregisterOnSharedPreferenceChangeListener(watchListener)
        super.onPause()
        pulseAnimator?.pause()
    }

    override fun onStop() {
        cancelSharePreparation()
        super.onStop()
    }

    override fun onDestroy() {
        cancelSharePreparation()
        shareDialog?.dismiss()
        shareDialog = null
        shareConfirmDialog?.dismiss()
        shareConfirmDialog = null
        uiRequests.invalidate()
        captureCancellation.cancel()
        super.onDestroy()
        pulseAnimator?.cancel()
    }

    private fun showPaired(paired: Boolean) {
        binding.panelUnpaired.visibility = if (paired) View.GONE else View.VISIBLE
        binding.barServerAddress.visibility =
            if (paired && selectedRepoId != null) View.VISIBLE else View.GONE
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
        val session = sessionKey()
        val ticket = beginUiRequest("activation", getString(R.string.status_activating))
        io.execute {
            try {
                val dial = DialAddress.resolve(address)
                val newClient = Androidbind.newClient(filesDir.absolutePath, dial, FileesSession.MOBILE_USER, hostKey)
                postCurrent(ticket) {
                    if (prefs.getString(FileesSession.PREF_ADDRESS, null) != address ||
                        prefs.getString(FileesSession.PREF_HOST_KEY, null) != hostKey) return@postCurrent
                    client = newClient
                    activeSession = session
                    watched = WatchedFolders(this)
                    showPaired(true)
                    requestBusy(ticket, false, "")
                    loadRealmProjection()
                    scanWatchedFolders()
                    refreshDecisions()
                }
            } catch (e: Exception) {
                postCurrent(ticket) {
                    if (prefs.getString(FileesSession.PREF_ADDRESS, null) != address ||
                        prefs.getString(FileesSession.PREF_HOST_KEY, null) != hostKey) return@postCurrent
                    client = null
                    activeSession = null
                    showPaired(false)
                    failRequest(ticket, getString(R.string.error_connect), e)
                }
            }
        }
    }

    private fun loadRealmProjection() {
        val active = client ?: return
        val serverId = FileesSession.current(prefs)?.id
        val ticket = beginUiRequest("projection", getString(R.string.status_refreshing))
        io.execute {
            try {
                val projection = RealmProjection.fromJson(active.listRepositoriesJSON())
                val drawers = try {
                    DrawerFrame.fromJson(active.listDrawersJSON())
                } catch (_: Exception) {
                    DrawerFrame.empty()
                }
                postCurrent(ticket) {
                    if (client !== active || FileesSession.current(prefs)?.id != serverId) return@postCurrent
                    drawerFrame = drawers
                    FileesSession.rememberProjection(prefs, projection, serverId)
                    bindServerLabel()
                    selectableShares = projection.shares.filter { it.selectable }
                    if (selectedRepoId != null && selectableShares.none { it.repoId == selectedRepoId }) {
                        selectedRepoId = null
                    }
                    renderList()
                    requestBusy(ticket, false, "")
                    refreshDecisions()
                }
            } catch (e: Exception) {
                postCurrent(ticket) {
                    if (client !== active || FileesSession.current(prefs)?.id != serverId || !uiRequests.ownsBusy(ticket)) return@postCurrent
                    if (looksRevoked(e)) showRevoked(e) else failBusy(getString(R.string.error_list), e)
                }
            }
        }
    }

    private fun renderList() {
        if (selectedRepoId == null) {
            selectedShareName = ""
            browsePrefix = ""
            browseAdapter.submit(homeRows())
            binding.toolbar.title = null
            binding.toolbar.subtitle = null
            binding.textBrowseState.visibility = View.GONE
            binding.brandLockup.visibility = View.VISIBLE
            binding.buttonAdd.visibility = View.GONE
            binding.barServerAddress.visibility = View.GONE
            supportActionBar?.setDisplayHomeAsUpEnabled(false)
            return
        }
        binding.barServerAddress.visibility = View.VISIBLE
        binding.brandLockup.visibility = View.GONE
        binding.buttonAdd.visibility = if (canCaptureSelected()) View.VISIBLE else View.GONE
        supportActionBar?.setDisplayHomeAsUpEnabled(true)
        binding.toolbar.title = if (browsePrefix.isEmpty()) selectedShareName else browsePrefix.substringAfterLast('/')
        val rows = mutableListOf<BrowseRow>()
        rows.add(factsRow())
        rows.addAll(ManifestBrowse.children(manifestEntries, browsePrefix))
        browseAdapter.submit(rows)
        bindBrowseState()
    }

    private fun homeRows(): List<BrowseRow> {
        val current = FileesSession.current(prefs)
        val servers = FileesSession.servers(prefs)
        val copy = if (current == null) {
            getString(R.string.home_copy_idle)
        } else {
            getString(R.string.home_copy_paired)
        }
        val rows = mutableListOf<BrowseRow>()
        rows.add(
            BrowseRow(
                "", "", directory = false, size = 0,
                kind = BrowseRow.Kind.HERO,
                heroCopy = copy,
            ),
        )
        rows.add(
            BrowseRow(
                "", "", directory = false, size = 0,
                kind = BrowseRow.Kind.METRICS,
                metricServers = servers.size.toString(),
                metricRepos = selectableShares.size.toString(),
                metricPending = watchStatus.summary(WatchStatusStore.scope(this),watched.uris()).first,
                metricPendingNote = watchStatus.summary(WatchStatusStore.scope(this),watched.uris()).second,
            ),
        )
        for (server in servers) {
            val active = server.id == current?.id
            val folders = if (active) shareRows(selectableShares) else emptyList()
            rows.add(
                BrowseRow(
                    name = server.displayName.ifBlank { server.address },
                    path = "",
                    directory = false,
                    size = 0,
                    kind = BrowseRow.Kind.SERVER,
                    // The metrics panel already gives the folder count. A named
                    // server shows its address here instead; an unnamed one
                    // already has the address as its title, so the count stays.
                    serverMeta = when {
                        !active -> getString(R.string.home_server_switch)
                        server.displayName.isNotBlank() -> server.address
                        else -> resources.getQuantityString(
                            R.plurals.home_folder_count,
                            selectableShares.size,
                            selectableShares.size,
                        )
                    },
                    switchServerId = if (active) "" else server.id,
                    accentColor = server.leadingColor,
                    panel = if (folders.isEmpty()) BrowseRow.Panel.SINGLE else BrowseRow.Panel.TOP,
                ),
            )
            rows.addAll(inPanel(folders))
        }
        rows.add(
            BrowseRow(
                "", "", directory = false, size = 0,
                kind = BrowseRow.Kind.ADD_SERVER,
            ),
        )
        val journal = journalRows()
        rows.add(
            BrowseRow(
                "", "", directory = false, size = 0,
                kind = BrowseRow.Kind.JOURNAL_HEAD,
                panel = if (journal.isEmpty()) BrowseRow.Panel.SINGLE else BrowseRow.Panel.TOP,
            ),
        )
        rows.addAll(inPanel(journal))
        return rows
    }

    // Rows that continue the panel opened by the item before them: the last
    // closes the card, the rest sit in its middle.
    private fun inPanel(members: List<BrowseRow>): List<BrowseRow> =
        members.mapIndexed { index, row ->
            row.copy(
                panel = if (index == members.lastIndex) BrowseRow.Panel.BOTTOM else BrowseRow.Panel.MIDDLE,
            )
        }

    private fun journalRows(): List<BrowseRow> {
        val rows = mutableListOf<BrowseRow>()
        val scope = prefs.getString(FileesSession.PREF_UPLOAD_REPO_NAME, null)
            ?.ifBlank { null }
            ?: selectedShareName
        for (item in pendingDecisions) {
            rows.add(
                BrowseRow(
                    "", "", directory = false, size = 0,
                    kind = BrowseRow.Kind.JOURNAL,
                    journalEntry = getString(R.string.home_journal_pending, item.filename),
                    journalScope = scope,
                ),
            )
        }
        FileesSession.sweepJournal(prefs, FileesSession.journalArchive(this))
        val history = FileesSession.journal(prefs)
        // Collapsed to the newest few; the rest (up to the retention cap) opens on tap.
        val shown = if (journalExpanded) history else history.take(JOURNAL_COLLAPSED)
        for (entry in shown) {
            rows.add(
                BrowseRow(
                    "", "", directory = false, size = entry.at,
                    kind = BrowseRow.Kind.JOURNAL,
                    journalEntry = entry.entry,
                    journalScope = entry.scope,
                ),
            )
        }
        if (history.size > JOURNAL_COLLAPSED) {
            rows.add(
                BrowseRow(
                    "", "", directory = false, size = 0,
                    kind = BrowseRow.Kind.JOURNAL,
                    journalEntry = if (journalExpanded) getString(R.string.home_journal_show_less)
                    else getString(R.string.home_journal_show_all, history.size),
                    journalToggle = true,
                ),
            )
        }
        return rows
    }

    private fun factsRow(): BrowseRow {
        val share = selectedShare()
        val cur = FileesSession.current(prefs)
        val access = when (share?.access) {
            "rw" -> getString(R.string.home_access_rw)
            "r" -> getString(R.string.home_access_r)
            else -> share?.access.orEmpty().ifBlank { getString(R.string.home_fact_revision_unknown) }
        }
        val state = when (share?.state) {
            "active" -> getString(R.string.home_state_active)
            "initializing" -> getString(R.string.home_state_initializing)
            else -> share?.state.orEmpty().ifBlank { getString(R.string.home_fact_revision_unknown) }
        }
        return BrowseRow(
            "", "", directory = false, size = 0,
            kind = BrowseRow.Kind.FACTS,
            factServer = cur?.displayName?.ifBlank { cur.address }.orEmpty()
                .ifBlank { getString(R.string.home_fact_revision_unknown) },
            factRevision = if (browseRevision > 0) "r$browseRevision" else getString(R.string.home_fact_revision_unknown),
            factAccess = access,
            factFolder = state,
        )
    }

    // Groups the top-level share list by purpose (ordinary repositories,
    // upload shelves, quarantine) instead of listing every kind mixed
    // together - the desktop projection already makes this distinction
    // (implementation notes (not distributed) §E2). A section header is only
    // inserted when more than one group is actually present, so a realm
    // with no upload shelves at all renders exactly as before.
    private fun shareRows(shares: List<RealmShare>): List<BrowseRow> {
        if (drawerFrame.isEmpty()) return shareRowsByPurpose(shares)
        val rows = mutableListOf<BrowseRow>()
        val placed = mutableSetOf<String>()
        for ((id, name) in drawerFrame.drawers) {
            val members = shares.filter { drawerFrame.assignments[it.repoId] == id }
            if (members.isEmpty()) continue
            val collapsed = isDrawerCollapsed(id)
            val mark = if (collapsed) "▸" else "▾"
            rows.add(sectionHeader("$mark  $name    ${members.size}", id))
            if (!collapsed) {
                members.forEach {
                    rows.add(shareRow(it))
                    placed.add(it.repoId)
                }
            } else {
                placed.addAll(members.map { it.repoId })
            }
        }
        val loose = shares.filter { it.repoId !in placed }
        if (rows.isEmpty()) return shareRowsByPurpose(loose)
        rows.addAll(shareRowsByPurpose(loose))
        return rows
    }

    private fun shareRowsByPurpose(shares: List<RealmShare>): List<BrowseRow> {
        val groups = shares.groupBy { it.purpose }
        if (groups.size <= 1) {
            return shares.map { share -> shareRow(share) }
        }
        val order = listOf("", "upload_shelf", "upload_trash")
        val rows = mutableListOf<BrowseRow>()
        for (purpose in order + (groups.keys - order.toSet())) {
            val members = groups[purpose] ?: continue
            rows.add(sectionHeader(sectionLabel(purpose)))
            members.forEach { rows.add(shareRow(it)) }
        }
        return rows
    }

    private fun sectionHeader(title: String, drawerId: String = ""): BrowseRow =
        BrowseRow(
            "", "", directory = false, size = 0,
            sectionHeader = title, drawerId = drawerId, kind = BrowseRow.Kind.HEADER,
        )

    private fun isDrawerCollapsed(id: String): Boolean {
        if (!prefs.contains(prefsCollapsed)) return true
        return prefs.getStringSet(prefsCollapsed, emptySet())?.contains(id) == true
    }

    private fun toggleDrawer(id: String) {
        val collapsed = if (!prefs.contains(prefsCollapsed)) {
            drawerFrame.drawers.map { it.first }.toMutableSet()
        } else {
            prefs.getStringSet(prefsCollapsed, emptySet())?.toMutableSet() ?: mutableSetOf()
        }
        if (id in collapsed) collapsed.remove(id) else collapsed.add(id)
        prefs.edit().putStringSet(prefsCollapsed, collapsed).apply()
        if (selectedRepoId == null) renderList()
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
        if (row.journalToggle) {
            journalExpanded = !journalExpanded
            browseAdapter.submit(homeRows())
            return
        }
        if (row.kind == BrowseRow.Kind.METRICS) {
            startActivity(Intent(this,SettingsActivity::class.java))
            return
        }
        if (row.kind == BrowseRow.Kind.HEADER && row.drawerId.isNotEmpty()) {
            toggleDrawer(row.drawerId)
            return
        }
        if (row.kind == BrowseRow.Kind.ADD_SERVER) {
            onScanQrClicked()
            return
        }
        if (row.kind == BrowseRow.Kind.SERVER && row.switchServerId.isNotEmpty()) {
            FileesSession.servers(prefs).firstOrNull { it.id == row.switchServerId }?.let { switchTo(it) }
            return
        }
        if (row.share) {
            invalidateUiRequests()
            selectedRepoId = row.repoId
            selectedShareName = row.name
            FileesSession.setSelectedRepo(prefs, row.repoId)
            browsePrefix = ""
            refreshAndList()
            refreshDecisions()
            return
        }
        if (row.directory) {
            invalidateUiRequests()
            browsePrefix = row.path
            listCurrentDir()
            return
        }
        previewRow(row)
    }

    private fun goUp(): Boolean {
        if (selectedRepoId == null) return false
        invalidateUiRequests()
        if (browsePrefix.isNotEmpty()) {
            browsePrefix = browsePrefix.substringBeforeLast('/', "")
            listCurrentDir()
            return true
        }
        selectedRepoId = null
        FileesSession.setSelectedRepo(prefs, null)
        clearBrowseConfirmation()
        renderList()
        return true
    }

    private fun refreshAndList() {
        openDirectory(force = true)
    }

    private fun listCurrentDir() {
        if (legacyFullTree) {
            renderList()
            return
        }
        openDirectory(force = false)
    }

    // The folder on screen comes from the local directory manifest. The server
    // is asked only when that page is missing, behind the confirmed pin, or
    // this open has to check whether the header moved.
    private fun openDirectory(force: Boolean) {
        val active = client ?: return
        val repoId = selectedRepoId ?: return
        val prefix = browsePrefix
        val ticket = uiRequests.begin("browse")
        val offline = browseOffline
        val knownRevision = if (confirmedRepoId == repoId) confirmedRevision else 0L
        val knownGeneration = if (confirmedRepoId == repoId) confirmedGeneration else 0L
        io.execute {
            val cached = try {
                active.cachedDirectoryJSON(repoId, prefix)
            } catch (_: Exception) {
                ""
            }
            val pageCurrent = cachedMatches(cached, knownGeneration, knownRevision)
            val needHeader = force || offline || knownRevision < 1
            if (cached.isNotBlank()) {
                postCurrent(ticket) { applyDirectoryManifest(cached) }
            } else {
                postCurrent(ticket) {
                    if (!uiRequests.current(ticket)) return@postCurrent
                    setBusy(true, getString(R.string.status_refreshing))
                    uiRequests.ownBusy(ticket)
                }
            }
            if (!needHeader && pageCurrent) return@execute
            try {
                val listing = active.syncDirectoryJSON(repoId, prefix, knownRevision, knownGeneration, needHeader)
                val listed = JSONObject(listing)
                val listedRev = listed.optLong("repo_revision")
                val listedGen = listed.optLong("view_generation")
                val shouts = ManifestBrowse.shoutsFrom(listing).let { all ->
                    if (listedRev > 0) all.filter { it.first <= listedRev } else all
                }
                postCurrent(ticket) {
                    browseOffline = false
                    confirmedRepoId = repoId
                    if (listedRev > 0) confirmedRevision = listedRev
                    if (listedGen > 0) confirmedGeneration = listedGen
                    applyDirectoryManifest(listing)
                    showNewShouts(repoId, shouts)
                    requestBusy(ticket, false, "")
                }
            } catch (e: Exception) {
                if (!isUnsupported(e)) {
                    if (cached.isNotBlank() && !requiresBrowseError(e)) {
                        postCurrent(ticket) {
                            browseOffline = true
                            applyDirectoryManifest(cached)
                        }
                    } else {
                        postCurrent(ticket) {
                            if (cached.isNotBlank()) {
                                clearBrowseConfirmation()
                                manifestEntries = emptyList()
                                renderList()
                                requestBusy(ticket, false, "")
                                if (looksRevoked(e)) showRevoked(e)
                                else showTransportError(getString(R.string.error_refresh), e, prefs.getString(FileesSession.PREF_ADDRESS, null))
                            } else failRequest(ticket, getString(R.string.error_refresh), e)
                        }
                    }
                    return@execute
                }
                try {
                    val json = active.refreshJSON(repoId)
                    postCurrent(ticket) {
                        browseOffline = false
                        legacyFullTree = true
                        browseRevision = if (json.isBlank()) 0L else JSONObject(json).optLong("repo_revision")
                        browseGeneration = if (json.isBlank()) 0L else JSONObject(json).optLong("view_generation")
                        manifestEntries = ManifestBrowse.entriesFrom(json)
                        renderList()
                        showNewShouts(repoId, ManifestBrowse.shoutsFrom(json))
                        requestBusy(ticket, false, "")
                    }
                } catch (e2: Exception) {
                    postCurrent(ticket) {
                        if (cached.isNotBlank()) {
                            requestBusy(ticket, false, "")
                            showTransportError(getString(R.string.error_refresh), e2, prefs.getString(FileesSession.PREF_ADDRESS, null))
                        } else failRequest(ticket, getString(R.string.error_refresh), e2)
                    }
                }
            }
        }
    }

    private fun applyDirectoryManifest(json: String) {
        val listed = JSONObject(json)
        val rev = listed.optLong("repo_revision")
        val gen = listed.optLong("view_generation")
        if (rev > 0) browseRevision = rev
        if (gen > 0) browseGeneration = gen
        legacyFullTree = false
        manifestEntries = ManifestBrowse.entriesFrom(json)
        renderList()
    }

    private fun cachedMatches(json: String, generation: Long, revision: Long): Boolean {
        if (json.isBlank() || generation < 1 || revision < 1) return false
        return try {
            val listed = JSONObject(json)
            listed.optLong("view_generation") == generation && listed.optLong("repo_revision") == revision
        } catch (_: Exception) {
            false
        }
    }

    private fun bindBrowseState() {
        if (selectedRepoId != null && browseOffline) {
            binding.textBrowseState.text = getString(R.string.browse_last_known)
            binding.textBrowseState.visibility = View.VISIBLE
        } else {
            binding.textBrowseState.visibility = View.GONE
        }
    }

    private fun clearBrowseConfirmation() {
        confirmedRepoId = null
        confirmedRevision = 0
        confirmedGeneration = 0
        browseOffline = false
        bindBrowseState()
    }

    private fun isUnsupported(e: Exception): Boolean {
        val text = (e.message ?: "").lowercase()
        return "op.unsupported" in text || "operation not supported" in text
    }

    private fun requiresBrowseError(e: Exception): Boolean {
        val text = (e.message ?: "").lowercase()
        return looksRevoked(e) || "mobile operation failed:" in text ||
            "host key mismatch" in text || "manifest rollback rejected" in text
    }

    private fun refreshManifest() {
        refreshAndList()
    }

    private fun bindServerLabel() {
        binding.textServerAddress.text = FileesSession.barLabel(prefs)
    }

    private fun selectedShare(): RealmShare? =
        selectableShares.firstOrNull { it.repoId == selectedRepoId }

    // Capture (Dodaj / watched folders) writes mobile-uploads/. Trash is
    // a reject waiting room: still listed and browsable, never a dump target.
    private fun canCaptureSelected(): Boolean = selectedShare()?.canCapture == true

    private fun captureTarget(): CaptureDestination<Client>? {
        val active = client ?: return null
        val repoId = selectedRepoId?.takeIf { it.isNotBlank() && canCaptureSelected() } ?: return null
        return CaptureDestination(active, repoId)
    }

    private fun takePickerTarget(): CaptureDestination<Client>? {
        val target = pickerTarget
        pickerTarget = null
        // A recreated Activity or changed pairing cannot reassign a picker result.
        return target?.takeIf { it.client === client && it.repoId == selectedRepoId }
    }

    private fun showAddChooser() {
        val target = captureTarget() ?: return
        AlertDialog.Builder(this)
            .setItems(arrayOf(getString(R.string.action_add_files), getString(R.string.action_add_folder))) { _, which ->
                pickerTarget = target
                if (which == 0) pickFilesLauncher.launch(arrayOf("*/*"))
                else pickFolderLauncher.launch(null)
            }
            .show()
    }

    private fun enqueueFolder(target: CaptureDestination<Client>, treeUri: Uri) {
        val ticket = beginUiRequest("capture", getString(R.string.status_scanning))
        io.execute {
            val files = try {
                DocumentWalk.tree(contentResolver, treeUri)
            } catch (e: Exception) {
                postCurrent(ticket) { failRequest(ticket, getString(R.string.error_send), e) }
                return@execute
            }
            if (files.isEmpty()) {
                postCurrent(ticket) { requestBusy(ticket, false, getString(R.string.browse_empty)) }
                return@execute
            }
            val summary = FolderPreflight.of(files)
            postCurrent(ticket) { requestBusy(ticket, true, preflightLabel(summary)) }
            sendCaptured(target, ticket, files, summary.pack)
        }
    }

    private fun preflightLabel(summary: FolderPreflight.Summary): String {
        val weight = HumanSize.format(summary.bytes)
        return resources.getQuantityString(R.plurals.status_preflight, summary.files, summary.files, weight)
    }

    private fun enqueueWalked(target: CaptureDestination<Client>, files: List<WalkedFile>) {
        if (files.isEmpty()) return
        val summary = FolderPreflight.of(files)
        val ticket = beginUiRequest("capture", preflightLabel(summary))
        io.execute {
            sendCaptured(target, ticket, files, summary.pack)
        }
    }

    private val captureCancellation = CaptureCancellation()

    private data class DownloadTarget(val client: Client, val server: String, val repo: String,
                                      val revision: Long, val generation: Long,
                                      val address: String, val hostKey: String)

    private fun downloadTarget(): DownloadTarget? {
        if (activeSession != sessionKey()) return null
        return DownloadTarget(client ?: return null, FileesSession.current(prefs)?.id ?: return null,
            selectedRepoId ?: return null, browseRevision, browseGeneration,
            activeSession?.second ?: return null, activeSession?.third ?: return null)
    }

    private fun shareTarget(task: ShareTask, target: DownloadTarget): DownloadTarget {
        task.check()
        val shareClient = task.client ?: Androidbind.newClient(
            filesDir.absolutePath, DialAddress.resolve(target.address), FileesSession.MOBILE_USER, target.hostKey,
        ).also { task.attach(it) }
        task.check()
        return target.copy(client = shareClient)
    }

    private fun sendCaptured(target: CaptureDestination<Client>, ticket: UiRequests.Ticket, files: List<WalkedFile>, packed: Boolean) {
        try {
            val result = CaptureTransfers.send(this, target.client, target.repoId, files, packed, captureCancellation)
            postCurrent(ticket) {
                if (!uiRequests.ownsBusy(ticket)) return@postCurrent
                if (result.errors.isNotEmpty()) {
                    failBusy(getString(R.string.error_send_partial, result.sent, files.size), java.io.IOException(result.errors.distinct().take(5).joinToString("\n")))
                } else {
                    setBusy(false, getString(R.string.status_sent_count, result.sent))
                }
                refreshManifest()
                refreshDecisions()
            }
        } catch (e: Exception) {
            postCurrent(ticket) { failRequest(ticket, getString(R.string.error_send), e) }
        }
    }

    private fun previewRow(row: BrowseRow) {
        val target = downloadTarget() ?: return
        val ticket = beginUiRequest("download", getString(R.string.status_downloading))
        io.execute {
            try {
                val dest = cachedDownload(target, row)
                postCurrent(ticket) {
                    requestBusy(ticket, false, "")
                    openCached(dest, row.name, target.server)
                }
            } catch (e: Exception) {
                postCurrent(ticket) { failRequest(ticket, getString(R.string.error_download), e) }
            }
        }
    }

    private fun downloadRow(row: BrowseRow) {
        if (row.directory) {
            downloadFolder(row)
            return
        }
        val target = downloadTarget() ?: return
        val ticket = beginUiRequest("download", getString(R.string.status_downloading))
        executeDownload(ticket) {
            var dest: File? = null
            try {
                dest = cachedDownload(target, row)
                DownloadCache.checkActive(cacheDir, target.server)
                publishDownload(dest, row.name)
                postCurrent(ticket) { requestBusy(ticket, false, getString(R.string.status_downloaded, row.name)) }
            } catch (e: Exception) {
                postCurrent(ticket) { failRequest(ticket, getString(R.string.error_download), e) }
            } finally {
                dest?.parentFile?.let { DownloadCache.removeAttempt(cacheDir, it) }
            }
        }
    }

    private fun beginSharePreparation(ticket: UiRequests.Ticket, message: String): ShareTask {
        val task = ShareTask(ticket, SharePackageLimit.bytes(this))
        shareTask = task
        showSharePreparation(task, message)
        return task
    }

    private fun showSharePreparation(task: ShareTask, message: String) {
        if (shareTask !== task || isFinishing || isDestroyed) return
        shareDialog?.dismiss()
        val density = resources.displayMetrics.density
        val padding = (20 * density).toInt()
        val spinnerSize = (40 * density).toInt()
        val progress = android.widget.FrameLayout(this).apply {
            setPadding(padding, padding, padding, padding)
            addView(android.widget.ProgressBar(this@MainActivity), android.widget.FrameLayout.LayoutParams(
                spinnerSize, spinnerSize, android.view.Gravity.CENTER,
            ))
        }
        val dialog = AlertDialog.Builder(this)
            .setTitle(R.string.action_share)
            .setMessage(message)
            .setView(progress)
            .setNegativeButton(R.string.action_cancel, null)
            .setCancelable(false)
            .create()
        dialog.show()
        dialog.getButton(AlertDialog.BUTTON_NEGATIVE).setOnClickListener { cancelSharePreparation() }
        shareDialog = dialog
    }

    private fun cancelSharePreparation() {
        val task = shareTask ?: return
        task.cancel()
        val confirming = shareConfirmDialog != null
        shareConfirmDialog?.dismiss()
        shareConfirmDialog = null
        shareDialog?.setMessage(getString(R.string.status_cancelling_share))
        shareDialog?.getButton(AlertDialog.BUTTON_NEGATIVE)?.isEnabled = false
        if (confirming) shareFailed(task, InterruptedIOException("Share cancelled"))
    }

    private fun clearSharePreparation(task: ShareTask) {
        if (shareTask !== task) return
        task.close()
        shareDialog?.dismiss()
        shareDialog = null
        shareConfirmDialog?.dismiss()
        shareConfirmDialog = null
        shareTask = null
    }

    private fun shareFailed(task: ShareTask, error: Exception) {
        postCurrent(task.ticket) {
            if (shareTask !== task) return@postCurrent
            clearSharePreparation(task)
            if (task.cancelled) requestBusy(task.ticket, false, getString(R.string.status_share_cancelled))
            else if (error is SharePackageTooLarge) {
                requestBusy(task.ticket, false, "")
                AlertDialog.Builder(this)
                    .setMessage(getString(R.string.share_folder_limit_exceeded,
                        HumanSize.format(error.actual), HumanSize.format(error.limit)))
                    .setPositiveButton(android.R.string.ok, null)
                    .show()
            }
            else failRequest(task.ticket, getString(R.string.error_share), error)
        }
    }

    private fun shareRow(row: BrowseRow) {
        if (row.share) return
        if (shareTask != null) return
        if (row.directory) {
            prepareFolder(row, share = true)
            return
        }
        val target = downloadTarget() ?: return
        val ticket = beginUiRequest("download", getString(R.string.status_preparing_share))
        val task = beginSharePreparation(ticket, getString(R.string.status_preparing_share))
        io.execute {
            var dest: File? = null
            try {
                val ready = cachedDownload(shareTarget(task, target), row)
                dest = ready
                task.check()
                DownloadCache.checkActive(cacheDir, target.server)
                dest = null // The chooser's recipient may read after this task ends.
                showShareWhenReady(task, target.server, ready, row.name, folder = false)
            } catch (e: Exception) {
                shareFailed(task, e)
            } finally {
                dest?.parentFile?.let { DownloadCache.removeAttempt(cacheDir, it) }
            }
        }
    }

    private fun downloadFolder(row: BrowseRow) {
        prepareFolder(row, share = false)
    }

    private fun prepareFolder(row: BrowseRow, share: Boolean) {
        val target = downloadTarget() ?: return
        val ticket = beginUiRequest("download", getString(if (share) R.string.status_preparing_share else R.string.status_scanning))
        val task = if (share) beginSharePreparation(ticket, getString(R.string.status_scanning)) else null
        if (legacyFullTree) {
            confirmFolder(target, ticket, row, ManifestBrowse.filesUnder(manifestEntries, row.path), share)
            return
        }
        io.execute {
            var readTarget = target
            try {
                if (task != null) readTarget = shareTarget(task, target)
                val json = readTarget.client.listFilesUnderJSON(readTarget.repo, row.path, readTarget.revision, readTarget.generation)
                task?.check()
                val files = ManifestBrowse.entriesFrom(json)
                postCurrent(ticket) { confirmFolder(readTarget, ticket, row, files, share) }
            } catch (e: Exception) {
                if (task != null && task.cancelled) { shareFailed(task, e); return@execute }
                if (!isUnsupported(e)) {
                    if (task != null) shareFailed(task, e)
                    else postCurrent(ticket) { failRequest(ticket, getString(R.string.error_download), e) }
                    return@execute
                }
                try {
                    task?.check()
                    val files = ManifestBrowse.filesUnder(
                        ManifestBrowse.entriesFrom(readTarget.client.refreshJSON(readTarget.repo)),
                        row.path,
                    )
                    task?.check()
                    postCurrent(ticket) { confirmFolder(readTarget, ticket, row, files, share) }
                } catch (e2: Exception) {
                    if (task != null) shareFailed(task, e2)
                    else postCurrent(ticket) { failRequest(ticket, getString(R.string.error_download), e2) }
                }
            }
        }
    }

    private fun confirmFolder(target: DownloadTarget, ticket: UiRequests.Ticket, row: BrowseRow, files: List<ManifestEntry>, share: Boolean) {
        if (!uiRequests.current(ticket)) return
        val task = if (share) {
            shareTask?.takeIf { it.ticket === ticket } ?: return
        } else null
        if (task?.cancelled == true) {
            shareFailed(task, InterruptedIOException("Share cancelled"))
            return
        }
        requestBusy(ticket, false, "")
        if (files.isEmpty() && !share) {
            setBusy(false, getString(R.string.browse_empty))
            return
        }
        val limit = task?.limitBytes ?: 400L * 1024 * 1024
        var bytes = 0L
        var invalidSize = false
        for (file in files) {
            if (file.size < 0 || file.size > Long.MAX_VALUE - bytes) { invalidSize = true; break }
            bytes += file.size
        }
        if (invalidSize || (task == null && files.size > 200) || bytes > limit) {
            if (task != null) clearSharePreparation(task)
            setBusy(false, "")
            AlertDialog.Builder(this)
                .setMessage(if (invalidSize) getString(if (share) R.string.error_share else R.string.error_download)
                    else if (share) getString(R.string.share_folder_limit_exceeded, HumanSize.format(bytes), HumanSize.format(limit))
                    else getString(R.string.error_folder_too_big, HumanSize.format(bytes)))
                .setPositiveButton(android.R.string.ok, null)
                .show()
            return
        }
        val start = {
            if (uiRequests.current(ticket)) pullFolderZip(target, row, files, share)
        }
        if (share || files.size >= 40 || bytes >= 40L * 1024 * 1024) {
            if (task != null) {
                shareDialog?.dismiss()
                shareDialog = null
            }
            val summary = resources.getQuantityString(
                R.plurals.status_preflight, files.size, files.size, HumanSize.format(bytes),
            )
            val confirm = AlertDialog.Builder(this)
                .setTitle(if (share) R.string.share_folder_confirm_title else R.string.download_folder_confirm_title)
                .setMessage(if (share) getString(R.string.share_folder_confirm, row.name, summary, HumanSize.format(limit))
                    else getString(R.string.download_folder_confirm, row.name, summary))
                .setPositiveButton(if (share) R.string.action_share else R.string.action_download) { _, _ ->
                    if (task != null) shareConfirmDialog = null
                    start()
                }
                .setNegativeButton(R.string.action_cancel) { _, _ ->
                    if (task != null) cancelSharePreparation()
                }
                .show()
            if (task != null) {
                shareConfirmDialog = confirm
                confirm.setOnCancelListener { cancelSharePreparation() }
            }
            return
        }
        start()
    }

    private fun pullFolderZip(target: DownloadTarget, row: BrowseRow, files: List<ManifestEntry>, share: Boolean) {
        val task = if (share) shareTask ?: return else null
        val message = if (files.isEmpty()) getString(R.string.status_packing_download)
            else getString(R.string.status_downloading_folder, 1, files.size)
        val ticket = beginUiRequest("download", message)
        if (task != null) {
            task.ticket = ticket
            if (shareDialog == null) showSharePreparation(task, message)
        }
        val action = {
            val staged = ArrayList<Pair<String, File>>(files.size)
            var attempt: File? = null
            try {
                task?.check()
                val readTarget = if (task != null) shareTarget(task, target) else target
                val dir = DownloadCache.create(cacheDir, target.server)
                attempt = dir
                DownloadPublication.requireName("${row.name}.zip")
                val entries = ShareExport.relativeFiles(row.path, files)
                var downloadedBytes = 0L
                val limit = task?.limitBytes ?: 400L * 1024 * 1024
                for ((index, pair) in entries.withIndex()) {
                    task?.check()
                    val (entry, relative) = pair
                    postCurrent(ticket) {
                        if (task?.cancelled != true) {
                            val progress = getString(R.string.status_downloading_folder, index + 1, files.size)
                            requestBusy(ticket, true, progress)
                            if (task != null) shareDialog?.setMessage(progress)
                        }
                    }
                    val dest = File(dir, "part-$index")
                    DownloadCache.checkActive(cacheDir, target.server)
                    readTarget.client.downloadTo(readTarget.repo, entry.path, dest.absolutePath)
                    task?.check()
                    if (dest.length() > limit - downloadedBytes) {
                        if (task != null) throw SharePackageTooLarge(downloadedBytes + dest.length(), limit)
                        throw java.io.IOException("Folder exceeds download limit")
                    }
                    downloadedBytes += dest.length()
                    staged.add(relative to dest)
                }
                postCurrent(ticket) {
                    if (task?.cancelled != true) {
                        requestBusy(ticket, true, getString(R.string.status_packing_download))
                        if (task != null) shareDialog?.setMessage(getString(R.string.status_packing_download))
                    }
                }
                val zip = TreeZip.packNamed(staged, dir, "${row.name}.zip") { task?.check() }
                task?.check()
                if (task != null && zip.length() > limit) throw SharePackageTooLarge(zip.length(), limit)
                DownloadCache.checkActive(cacheDir, target.server)
                if (share) {
                    for ((_, part) in staged) {
                        if (!part.delete()) throw java.io.IOException("Cannot remove ZIP staging file")
                    }
                    dir.setLastModified(System.currentTimeMillis())
                    attempt = null // Keep the private file until the receiving app has read it.
                    showShareWhenReady(task!!, target.server, zip, "${row.name}.zip", folder = true)
                } else {
                    publishDownload(zip, "${row.name}.zip")
                    postCurrent(ticket) { requestBusy(ticket, false, getString(R.string.status_downloaded, "${row.name}.zip")) }
                }
            } catch (e: Exception) {
                if (task != null) shareFailed(task, e)
                else postCurrent(ticket) { failRequest(ticket, getString(R.string.error_download), e) }
            } finally {
                attempt?.let { DownloadCache.removeAttempt(cacheDir, it) }
            }
        }
        if (share) io.execute { action() } else executeDownload(ticket, action)
    }

    private fun showShareWhenReady(task: ShareTask, server: String, file: File, name: String, folder: Boolean) {
        main.post {
            val ticket = task.ticket
            if (isDestroyed || isFinishing || task.cancelled || shareTask !== task || !uiRequests.current(ticket)) {
                DownloadCache.removeAttempt(cacheDir, file.parentFile!!)
                if (!isDestroyed && !isFinishing && shareTask === task) {
                    clearSharePreparation(task)
                    requestBusy(ticket, false, getString(R.string.status_share_cancelled))
                }
                return@post
            }
            try {
                DownloadCache.checkActive(cacheDir, server)
                val send = ShareExport.sendIntent(this, file, name, folder)
                val chooser = Intent.createChooser(send, getString(R.string.action_share)).apply {
                    clipData = send.clipData
                    addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
                }
                startActivity(chooser)
                clearSharePreparation(task)
                requestBusy(ticket, false, "")
            } catch (e: Exception) {
                DownloadCache.removeAttempt(cacheDir, file.parentFile!!)
                clearSharePreparation(task)
                failRequest(ticket, getString(R.string.error_share), e)
            }
        }
    }

    private fun showNewShouts(repoId: String, shouts: List<Pair<Long, String>>) {
        val fresh = shouts.filter {
            !FileesSession.isShoutAcked(prefs, FileesSession.shoutId(repoId, it.first))
        }
        if (fresh.isEmpty()) return
        val scope = selectedShareName.ifBlank { repoId }
        val ids = fresh.map { FileesSession.shoutId(repoId, it.first) }
        for (item in fresh) {
            FileesSession.pushJournal(
                prefs,
                scope,
                getString(R.string.home_journal_shout, item.first.toString()) + "\n" + item.second,
                FileesSession.shoutId(repoId, item.first),
                FileesSession.journalArchive(this),
            )
        }
        FileesSession.ackShouts(prefs, ids)
        AlertDialog.Builder(this)
            .setTitle(R.string.shouts_title)
            .setMessage(fresh.joinToString("\n\n") { it.second })
            .setPositiveButton(R.string.shouts_ack, null)
            .show()
    }

    private fun cachedDownload(target: DownloadTarget, row: BrowseRow): File {
        DownloadPublication.requireName(row.name)
        val dir = DownloadCache.create(cacheDir, target.server)
        try {
            val dest = File(dir, row.name)
            target.client.downloadTo(target.repo, row.path, dest.absolutePath)
            DownloadCache.checkActive(cacheDir, target.server)
            dir.setLastModified(System.currentTimeMillis())
            return dest
        } catch (e: Exception) {
            DownloadCache.removeAttempt(cacheDir, dir)
            throw e
        }
    }

    private fun openCached(file: File, name: String, server: String) {
        val mime = MimeTypeMap.getSingleton().getMimeTypeFromExtension(file.extension.lowercase())
            ?: "application/octet-stream"
        val uri = FileProvider.getUriForFile(this, "$packageName.files", file)
        val intent = Intent(Intent.ACTION_VIEW)
            .setDataAndType(uri, mime)
            .addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
        try {
            startActivity(intent)
        } catch (_: ActivityNotFoundException) {
            val ticket = beginUiRequest("download", getString(R.string.status_downloading))
            executeDownload(ticket) {
                try {
                    DownloadCache.checkActive(cacheDir, server)
                    publishDownload(file, name)
                    postCurrent(ticket) { requestBusy(ticket, false, getString(R.string.error_preview)) }
                } catch (e: Exception) {
                    postCurrent(ticket) { failRequest(ticket, getString(R.string.error_download), e) }
                } finally {
                    DownloadCache.removeAttempt(cacheDir, file.parentFile!!)
                }
            }
        }
    }

    private fun executeDownload(ticket: UiRequests.Ticket, action: () -> Unit) {
        val start = { io.execute { action() } }
        if (Build.VERSION.SDK_INT >= 29 || ContextCompat.checkSelfPermission(this,
                Manifest.permission.WRITE_EXTERNAL_STORAGE) == PackageManager.PERMISSION_GRANTED) {
            start()
        } else {
            pendingDownloadPermission = ticket to start
            downloadPermission.launch(Manifest.permission.WRITE_EXTERNAL_STORAGE)
        }
    }

    private fun publishDownload(file: File, name: String) {
        DownloadPublication.requireName(name)
        val mime = MimeTypeMap.getSingleton().getMimeTypeFromExtension(file.extension.lowercase()) ?: "application/octet-stream"
        if (Build.VERSION.SDK_INT >= 29) {
            val values = ContentValues().apply {
                put(MediaStore.Downloads.DISPLAY_NAME, name)
                put(MediaStore.Downloads.MIME_TYPE, mime)
                put(MediaStore.Downloads.IS_PENDING, 1)
            }
            DownloadPublication.pending(
                input = { file.inputStream() },
                create = { contentResolver.insert(MediaStore.Downloads.EXTERNAL_CONTENT_URI, values) },
                open = { contentResolver.openOutputStream(it) },
                commit = { uri ->
                    val ready = ContentValues().apply { put(MediaStore.Downloads.IS_PENDING, 0) }
                    contentResolver.update(uri, ready, null, null)
                },
                remove = { uri ->
                    if (contentResolver.delete(uri, null, null) != 1) throw java.io.IOException("Cannot remove incomplete Downloads entry")
                },
            )
            return
        }
        val publicDir = Environment.getExternalStoragePublicDirectory(Environment.DIRECTORY_DOWNLOADS)
        DownloadPublication.legacy(file, publicDir, name)
    }

    private fun uploadRepoId(): String? =
        selectedRepoId?.takeIf { canCaptureSelected() }
            ?: prefs.getString(FileesSession.PREF_UPLOAD_REPO_ID, null)

    private fun refreshDecisions() {
        val ticket = uiRequests.begin("decisions")
        val active = client ?: run {
            bindDecisions(emptyList())
            return
        }
        val repoId = uploadRepoId() ?: run {
            bindDecisions(emptyList())
            return
        }
        val captureScope = WatchStatusStore.scope(this)
        io.execute {
            try {
                val all = PendingUpload.listFromJson(active.listUploadsJSON(repoId))
                postCurrent(ticket) {
                    if (client !== active || repoId != uploadRepoId() || captureScope != WatchStatusStore.scope(this)) return@postCurrent
                    if (repoId == prefs.getString(FileesSession.PREF_UPLOAD_REPO_ID, null)) {
                        watchStatus.updateSummary(captureScope, all)
                    }
                    bindDecisions(all.filter { it.needsDecision })
                }
            } catch (_: Exception) {
                postCurrent(ticket) {
                    if (client === active && repoId == uploadRepoId() && captureScope == WatchStatusStore.scope(this)) bindDecisions(emptyList())
                }
            }
        }
    }

    private fun bindDecisions(items: List<PendingUpload>) {
        pendingDecisions = items
        binding.panelDecisions.visibility = if (items.isEmpty()) View.GONE else View.VISIBLE
        pendingAdapter.submit(items)
        if (items.isNotEmpty()) {
            binding.textDecisionsHeader.text = getString(R.string.status_waiting_decision, items.size)
        }
        if (selectedRepoId == null && client != null) {
            browseAdapter.submit(homeRows())
        }
    }

    private fun discardPending(item: PendingUpload) {
        val active = client ?: return
        val repoId = uploadRepoId() ?: return
        val targetWatched = watched
        io.execute {
            try {
                CaptureCoordinator.run(captureCancellation) {
                    item.sources.forEach { targetWatched.markSeen(it, repoId) } // only this server/repository
                    active.discardUpload(repoId, item.id)
                }
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
                active.retryUploadAs(repoId, item.id, filename)
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
        invalidateUiRequests()
        FileesSession.select(prefs, server.id)
        watched = WatchedFolders(this, server.id)
        client = null
        selectedRepoId = FileesSession.current(prefs)?.selectedRepoId?.ifBlank { null }
        selectedShareName = ""
        browsePrefix = ""
        browseRevision = 0
        browseGeneration = 0
        legacyFullTree = false
        clearBrowseConfirmation()
        selectableShares = emptyList()
        drawerFrame = DrawerFrame.empty()
        manifestEntries = emptyList()
        bindDecisions(emptyList())
        bindServerLabel()
        activate(server.address, server.hostKey)
    }

    private fun unpairNow() {
        invalidateUiRequests()
        FileesSession.current(prefs)?.let { DownloadCache.forget(cacheDir, it.id) }
        FileesSession.unpair(prefs)
        watched = WatchedFolders(this)
        client = null
        activeSession = null
        selectedRepoId = null
        selectableShares = emptyList()
        drawerFrame = DrawerFrame.empty()
        manifestEntries = emptyList()
        browseRevision = 0
        browseGeneration = 0
        legacyFullTree = false
        clearBrowseConfirmation()
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
        // The screen does not need a live SSH client or a browsed repository.
        // The worker reads the paired upload target and every watched tree itself.
        FileesWatchScheduler.nudge(this)
    }

    private fun failBusy(headline: String, err: Exception) {
        setBusy(false, headline)
        showTransportError(headline, err, prefs.getString(FileesSession.PREF_ADDRESS, null))
    }

    // Capture identity only: display names / projection metadata can change
    // without retiring a request. Read on the UI thread, including callbacks.
    private fun sessionKey() = Triple(
        FileesSession.current(prefs)?.id,
        prefs.getString(FileesSession.PREF_ADDRESS, null),
        prefs.getString(FileesSession.PREF_HOST_KEY, null),
    )

    private fun invalidateUiRequests() {
        uiRequests.invalidate()
        pickerTarget = null
        pendingDownloadPermission = null
        setBusy(false, "")
    }

    private fun beginUiRequest(lane: String, message: String): UiRequests.Ticket {
        val ticket = uiRequests.begin(lane)
        setBusy(true, message)
        uiRequests.ownBusy(ticket)
        return ticket
    }

    private fun postCurrent(ticket: UiRequests.Ticket, action: () -> Unit) {
        main.post { if (!isDestroyed && !isFinishing && uiRequests.current(ticket)) action() }
    }

    private fun requestBusy(ticket: UiRequests.Ticket, busy: Boolean, message: String) {
        if (!uiRequests.ownsBusy(ticket)) return
        setBusy(busy, message)
        if (busy) uiRequests.ownBusy(ticket)
    }

    private fun failRequest(ticket: UiRequests.Ticket, headline: String, err: Exception) {
        if (uiRequests.ownsBusy(ticket)) failBusy(headline, err)
    }

    private fun setBusy(busy: Boolean, message: String) {
        uiRequests.clearBusy()
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
