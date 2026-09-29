package net.filees.mobile

import android.Manifest
import android.app.KeyguardManager
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.content.res.ColorStateList
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.text.TextUtils
import android.view.View
import android.widget.LinearLayout
import android.widget.TextView
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AlertDialog
import androidx.appcompat.app.AppCompatActivity
import androidx.core.content.ContextCompat
import androidx.core.content.edit
import androidbind.Androidbind
import androidbind.Client
import com.google.android.material.button.MaterialButton
import com.journeyapps.barcodescanner.ScanContract
import com.journeyapps.barcodescanner.ScanOptions
import net.filees.mobile.databinding.ActivitySettingsBinding
import org.json.JSONObject

class SettingsActivity : AppCompatActivity() {

    private lateinit var binding: ActivitySettingsBinding
    private lateinit var watched: WatchedFolders
    private var uploadRepos: List<RealmShare> = emptyList()
    private var uploadReposReady = false
    private var uploadReposError: String? = null
    private var drawerFrame: DrawerFrame = DrawerFrame.empty()
    private var watchChange = 0
    // A call to pickUploadTarget that arrived before loadUploadRepos finished
    // used to just dead-end on "still checking" - the caller (in particular
    // finishAddWatch's addThisWatch) never ran, so adding a watch silently
    // did nothing. Queued here instead and replayed once the load resolves,
    // success or failure, so the same click keeps working without the user
    // having to retry by hand (2026-09-26, reported: watched folders "still
    // don't work").
    private val pendingUploadTargetPicks = mutableListOf<(() -> Unit)?>()
    private var mobile: Client? = null
    private var pendingJoinEmail = ""
    private var joinInFlight = false

    private val scanLauncher = registerForActivityResult(ScanContract()) { result ->
        result.contents?.let { pairFromPayload(it) }
    }
    private val cameraPermissionLauncher = registerForActivityResult(ActivityResultContracts.RequestPermission()) { granted ->
        if (granted) launchScanner()
    }
    // Result ignored on purpose: a denial just means notifySent() (in
    // FileesWatchTick) silently skips posting later, same as any other
    // Android app whose notification permission was refused - there is
    // nothing sensible to do about it right here.
    private val notificationPermissionLauncher = registerForActivityResult(ActivityResultContracts.RequestPermission()) {}

    // concepts session 2026-08-29: adding a watch used to go straight from
    // "folder picked" to "silently uploading everything already in it" -
    // fine for a handful of new photos, not fine for a folder that already
    // holds gigabytes. Now it always scans first and asks.
    private val addWatchLauncher = registerForActivityResult(ActivityResultContracts.OpenDocumentTree()) { uri ->
        if (uri != null) confirmAndAddWatch(uri)
    }
    private val confirmDeviceLock = registerForActivityResult(ActivityResultContracts.StartActivityForResult()) { result ->
        if (result.resultCode == RESULT_OK) {
            sendJoinRequest(pendingJoinEmail)
            return@registerForActivityResult
        }
        joinInFlight = false
        binding.buttonRequestDesktopJoin.isEnabled = true
        joinAlert(getString(R.string.join_error_cancelled))
    }

    private val watchStatus by lazy { WatchStatusStore(this) }
    private val watchLabels = mutableMapOf<Uri, TextView>()
    private val watchListener = android.content.SharedPreferences.OnSharedPreferenceChangeListener { _, _ ->
        uiSafe { refreshWatchLabels() }
    }
    override fun onResume() {
        super.onResume()
        watchStatus.prefs.registerOnSharedPreferenceChangeListener(watchListener)
        refreshWatchLabels()
    }
    override fun onPause() {
        watchStatus.prefs.unregisterOnSharedPreferenceChangeListener(watchListener)
        super.onPause()
    }
    private fun refreshWatchLabels() {
        val scope = WatchStatusStore.scope(this)
        watchLabels.forEach { (uri,label) ->
            val state = watchStatus.state(scope,uri)
            label.text = "● ${watchStatus.label(state.phase)} · ${getString(R.string.watch_queue_count,state.waiting)}"
            label.setTextColor(androidx.core.content.ContextCompat.getColor(this,
                if(state.phase == "error") R.color.filees_destructive else R.color.filees_text))
        }
    }

    override fun attachBaseContext(newBase: Context) {
        super.attachBaseContext(FileesLocale.wrap(newBase))
    }

    // Every background Thread in this file posts its result back with this,
    // not raw runOnUiThread: under a slow/flaky connection a network call
    // can easily still be in flight when the user backs out of Settings.
    // The bare runOnUiThread{} still executes the callback once the
    // Activity is destroyed - AlertDialog.Builder(this).show() on a dead
    // Activity throws WindowManager.BadTokenException, which is exactly the
    // instability reported live under connection problems, 2026-09-26:
    // repeated attempts against a bad connection kept leaving delayed
    // callbacks armed, any one of which could fire onto a since-destroyed
    // screen. isFinishing/isDestroyed does not need minSdk gating (24 here,
    // both available since 17).
    private fun uiSafe(action: () -> Unit) {
        runOnUiThread { if (!isFinishing && !isDestroyed) action() }
    }

    private fun watchCurrent(): Boolean = watched.serverId == FileesSession.current(
        getSharedPreferences(FileesSession.PREFS, MODE_PRIVATE))?.id

    private fun watchUi(action: () -> Unit) = uiSafe { if (watchCurrent()) action() }

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
        binding.buttonAddWatched.setOnClickListener { addWatchLauncher.launch(null) }
        binding.buttonChangeUploadTarget.setOnClickListener { pickUploadTarget() }
        binding.buttonAddServer.setOnClickListener {
            if (ContextCompat.checkSelfPermission(this, Manifest.permission.CAMERA) == PackageManager.PERMISSION_GRANTED) {
                launchScanner()
            } else {
                cameraPermissionLauncher.launch(Manifest.permission.CAMERA)
            }
        }
        binding.buttonRequestDesktopJoin.setOnClickListener { confirmJoinThenSend() }
        binding.buttonAbout.setOnClickListener { showAbout() }
        if (intent.getBooleanExtra(AutoUpdate.EXTRA_ABOUT,false)) {
            intent.removeExtra(AutoUpdate.EXTRA_ABOUT)
            binding.root.post { if(!isFinishing && !isDestroyed) showAbout() }
        }
        binding.buttonAdvanced.setOnClickListener { showAdvanced() }

        val prefs = getSharedPreferences(FileesSession.PREFS, MODE_PRIVATE)
        FileesSession.migrate(prefs)
        val address = prefs.getString(FileesSession.PREF_ADDRESS, null)
        val hostKey = prefs.getString(FileesSession.PREF_HOST_KEY, null)
        binding.textDevicePublicKey.text = localPublicKey()
        if (!address.isNullOrBlank() && !hostKey.isNullOrBlank()) {
            // DialAddress.resolve does a real (blocking) DNS lookup for a
            // hostname address - the Go core does the equivalent off-thread
            // (MainActivity.activate runs this inside io.execute), but this
            // call sat directly in onCreate. On a real device that throws
            // NetworkOnMainThreadException, whose .message is always null,
            // so the fallback below showed a bare "Could not connect" with
            // no cause visible anywhere - reported live, 2026-09-26. An IP
            // literal address never triggers this (no real DNS I/O), which
            // is why emulator testing with 10.0.2.2 never caught it.
            Thread {
                try {
                    val client = Androidbind.newClient(
                        filesDir.absolutePath,
                        DialAddress.resolve(address),
                        FileesSession.MOBILE_USER,
                        hostKey,
                    )
                    uiSafe {
                        mobile = client
                        loadUploadRepos(client)
                    }
                } catch (e: Exception) {
                    // The key above does not depend on this connection, but a
                    // queued pickUploadTarget call does: without this,
                    // uploadReposReady never becomes true and every future tap
                    // re-queues into a dialog that can never resolve (reported
                    // live: "Where new files go" stays stuck forever, even
                    // across repeated taps, because loadUploadRepos never ran
                    // at all here - not the slow-network case r1613 fixed).
                    uiSafe {
                        uploadReposReady = true
                        uploadReposError = e.message?.ifBlank { null } ?: getString(R.string.error_connect)
                        resumePendingUploadTargetPicks()
                    }
                }
            }.start()
        } else {
            // No pairing at all: same reasoning as above, a queued pick must
            // still resolve to something instead of hanging forever.
            uploadReposReady = true
            uploadReposError = getString(R.string.error_connect)
        }
        bindServerDetails(prefs)
        renderServers()
        renderWatched()
        renderUploadTarget()
        bindLanguage()
        binding.buttonChangeLanguage.setOnClickListener { pickLanguage() }
    }

    private fun localPublicKey(): String {
        return try {
            Androidbind.publicKeyIn(filesDir.absolutePath).trim().ifBlank {
                getString(R.string.device_public_key_missing)
            }
        } catch (e: Exception) {
            e.message?.takeIf { it.isNotBlank() } ?: getString(R.string.device_public_key_missing)
        }
    }

    @Suppress("DEPRECATION")
    private fun appVersionName(): String {
        return try {
            val info = packageManager.getPackageInfo(packageName, 0)
            info.versionName?.takeIf { it.isNotBlank() } ?: getString(R.string.about_unknown)
        } catch (_: Exception) {
            getString(R.string.about_unknown)
        }
    }

    private fun showAdvanced() {
        val panel = binding.panelAdvanced
        val parent = panel.parent as android.view.ViewGroup
        val index = parent.indexOfChild(panel)
        parent.removeView(panel)
        panel.visibility = View.VISIBLE
        val scroll = android.widget.ScrollView(this).apply { addView(panel) }
        AlertDialog.Builder(this)
            .setTitle(R.string.settings_advanced)
            .setView(scroll)
            .setPositiveButton(R.string.action_close, null)
            .setOnDismissListener {
                scroll.removeView(panel)
                panel.visibility = View.GONE
                parent.addView(panel, index)
            }.show()
    }

    private fun showAbout() {
        val view = layoutInflater.inflate(R.layout.dialog_about, null)
        val version = appVersionName()
        view.findViewById<TextView>(R.id.textAboutClient).text = version
        view.findViewById<TextView>(R.id.textAboutChannel).text = getString(R.string.about_channel_apk)
        view.findViewById<TextView>(R.id.textAboutRelease).text = version
        view.findViewById<TextView>(R.id.textAboutStatus).text = getString(R.string.about_status_apk)
        val auto = view.findViewById<com.google.android.material.switchmaterial.SwitchMaterial>(R.id.switchAutoUpdate)
        auto.isChecked = AutoUpdate.enabled(this)
        auto.setOnCheckedChangeListener { _, value -> AutoUpdate.setEnabled(this,value) }
        val updateButton = view.findViewById<MaterialButton>(R.id.buttonCheckUpdate)
        val ready = AutoUpdate.ready(this)
        if (ready != null) {
            view.findViewById<TextView>(R.id.textAboutStatus).text = getString(R.string.update_available,ready.version)
            updateButton.setText(R.string.update_install)
            updateButton.setOnClickListener { fetchAndInstall(view.findViewById(R.id.textAboutStatus),updateButton,ready) }
        } else updateButton.setOnClickListener { checkUpdate(view.findViewById(R.id.textAboutStatus), updateButton) }
        val licenseBody = view.findViewById<TextView>(R.id.textAboutLicenseFull)
        val licenseToggle = view.findViewById<MaterialButton>(R.id.buttonAboutLicense)
        licenseToggle.setOnClickListener {
            val show = licenseBody.visibility != View.VISIBLE
            licenseBody.visibility = if (show) View.VISIBLE else View.GONE
            licenseToggle.setText(if (show) R.string.about_license_hide else R.string.about_license_full)
        }
        AlertDialog.Builder(this)
            .setView(view)
            .setPositiveButton(R.string.action_close, null)
            .show()
    }

    private fun checkUpdate(status: TextView, button: MaterialButton) {
        button.isEnabled = false
        status.setText(R.string.update_checking)
        Thread {
            try {
                val offer = ApkUpdate.inspect(this)
                uiSafe { presentUpdate(status, button, offer) }
            } catch (_: Exception) {
                uiSafe {
                    button.isEnabled = true
                    status.setText(R.string.update_failed)
                }
            }
        }.start()
    }

    private fun presentUpdate(status: TextView, button: MaterialButton, offer: ApkUpdate.Offer) {
        button.isEnabled = true
        when (offer.state) {
            "current" -> {
                status.setText(R.string.update_current)
                if (offer.sequence > 0) {
                    getSharedPreferences(FileesSession.PREFS, MODE_PRIVATE)
                        .edit()
                        .putLong(ApkUpdate.PREF_SEQUENCE, offer.sequence)
                        .apply()
                }
            }
            "absent" -> status.setText(R.string.update_absent)
            "available" -> {
                status.text = getString(R.string.update_available, offer.version)
                AlertDialog.Builder(this)
                    .setTitle(R.string.update_check)
                    .setMessage(getString(R.string.update_available, offer.version))
                    .setPositiveButton(R.string.update_install) { _, _ -> fetchAndInstall(status, button, offer) }
                    .setNegativeButton(R.string.action_cancel, null)
                    .show()
            }
            else -> status.text = offer.message.ifBlank { getString(R.string.update_failed) }
        }
    }

    private fun fetchAndInstall(status: TextView, button: MaterialButton, offer: ApkUpdate.Offer) {
        if (!ApkUpdate.canInstall(this)) {
            status.setText(R.string.update_need_permission)
            startActivity(ApkUpdate.unknownSourcesSettings(this))
            return
        }
        button.isEnabled = false
        status.setText(R.string.update_downloading)
        Thread {
            try {
                val apk = ApkUpdate.download(this, offer)
                val archiveCode = ApkUpdate.archiveVersionCode(this, apk)
                val installed = packageManager.getPackageInfo(packageName, 0).let { info ->
                    if (Build.VERSION.SDK_INT >= 28) info.longVersionCode else @Suppress("DEPRECATION") info.versionCode.toLong()
                }
                if (archiveCode <= installed) {
                    apk.delete()
                    if (offer.sequence > 0) {
                        getSharedPreferences(FileesSession.PREFS, MODE_PRIVATE)
                            .edit()
                            .putLong(ApkUpdate.PREF_SEQUENCE, offer.sequence)
                            .apply()
                    }
                    uiSafe {
                        button.isEnabled = true
                        status.setText(R.string.update_not_newer)
                    }
                    return@Thread
                }
                uiSafe {
                    button.isEnabled = true
                    status.setText(R.string.update_installing)
                    ApkUpdate.install(this, apk, offer.sequence)
                }
            } catch (_: Exception) {
                uiSafe {
                    button.isEnabled = true
                    status.setText(R.string.update_failed)
                }
            }
        }.start()
    }

    private fun bindLanguage() {
        binding.textLanguage.text = FileesLocale.displayName(this, FileesLocale.preference(this))
    }

    @Suppress("DEPRECATION")
    private fun confirmJoinThenSend() {
        if (joinInFlight) return
        val email = binding.editJoinEmail.text?.toString()?.trim().orEmpty()
        if (!looksLikeJoinEmail(email)) {
            joinAlert(getString(R.string.join_error_email))
            return
        }
        if (mobile == null) {
            joinAlert(getString(R.string.join_error_unpaired))
            return
        }
        val km = getSystemService(KEYGUARD_SERVICE) as KeyguardManager
        if (!km.isDeviceSecure) {
            joinAlert(getString(R.string.join_error_no_lock))
            return
        }
        val intent = km.createConfirmDeviceCredentialIntent(
            getString(R.string.join_pin_title),
            getString(R.string.join_pin_description),
        )
        if (intent == null) {
            joinAlert(getString(R.string.join_error_no_lock))
            return
        }
        pendingJoinEmail = email
        joinInFlight = true
        binding.buttonRequestDesktopJoin.isEnabled = false
        confirmDeviceLock.launch(intent)
    }

    private fun sendJoinRequest(email: String) {
        val client = mobile
        if (client == null) {
            joinInFlight = false
            binding.buttonRequestDesktopJoin.isEnabled = true
            joinAlert(getString(R.string.join_error_unpaired))
            return
        }
        binding.buttonRequestDesktopJoin.setText(R.string.join_sending)
        Thread {
            try {
                client.requestDesktopJoin(email)
                uiSafe {
                    joinFinished()
                    AlertDialog.Builder(this)
                        .setTitle(R.string.join_success_title)
                        .setMessage(getString(R.string.join_success, email))
                        .setPositiveButton(android.R.string.ok, null)
                        .show()
                }
            } catch (e: Exception) {
                uiSafe {
                    joinFinished()
                    joinAlert(joinErrorMessage(e))
                }
            }
        }.start()
    }

    private fun joinFinished() {
        joinInFlight = false
        binding.buttonRequestDesktopJoin.isEnabled = true
        binding.buttonRequestDesktopJoin.setText(R.string.action_request_desktop_join)
    }

    private fun joinErrorMessage(err: Exception): String {
        val raw = err.message.orEmpty()
        val text = raw.lowercase()
        if ("op.unsupported" in text || "operation not supported" in text) {
            return getString(R.string.join_error_unsupported)
        }
        if ("email must be" in text) {
            return getString(R.string.join_error_email)
        }
        val catalog = try {
            val lang = resources.configuration.locales[0].language
            Androidbind.explainIn(raw, lang)
        } catch (_: Exception) {
            try {
                Androidbind.explain(raw)
            } catch (_: Exception) {
                ""
            }
        }
        return catalog.ifBlank { getString(R.string.error_generic) }
    }

    private fun joinAlert(message: String) {
        AlertDialog.Builder(this)
            .setTitle(R.string.join_error_title)
            .setMessage(message)
            .setPositiveButton(android.R.string.ok, null)
            .show()
    }

    private fun looksLikeJoinEmail(value: String): Boolean {
        if (value.isEmpty() || value.length > 254) return false
        if (value.any { it.isWhitespace() }) return false
        val at = value.indexOf('@')
        return at in 1 until value.lastIndex && at == value.lastIndexOf('@')
    }

    private fun pickLanguage() {
        val codes = FileesLocale.pickerCodes()
        val labels = codes.map { FileesLocale.pickerLabel(this, it) }.toTypedArray()
        AlertDialog.Builder(this)
            .setTitle(R.string.settings_language)
            .setItems(labels) { _, index ->
                FileesLocale.applyChoice(this, codes[index])
            }
            .show()
    }

    private fun bindServerDetails(prefs: android.content.SharedPreferences) {
        val lines = mutableListOf<String>()
        val label = FileesSession.serverLabel(prefs)
        if (label.isNotBlank()) lines.add(getString(R.string.settings_server, label))
        val alias = prefs.getString(FileesSession.PREF_REALM_ALIAS, null)
        if (!alias.isNullOrBlank()) lines.add(getString(R.string.settings_realm, alias))
        val generated = prefs.getString(FileesSession.PREF_VIEW_GENERATED_AT, null)
        if (!generated.isNullOrBlank()) {
            val day = if (generated.length >= 10) generated.substring(0, 10) else generated
            lines.add(getString(R.string.settings_view_at, day))
        }
        binding.textDetails.text = when {
            lines.isNotEmpty() -> lines.joinToString("\n")
            else -> prefs.getString(FileesSession.PREF_DETAILS, "")
        }
    }

    private fun renderServers() {
        binding.listServers.removeAllViews()
        val prefs = getSharedPreferences(FileesSession.PREFS, MODE_PRIVATE)
        val all = FileesSession.servers(prefs)
        val currentId = FileesSession.current(prefs)?.id
        if (all.isEmpty()) {
            binding.listServers.addView(fileesMetaText(getString(R.string.status_idle)))
            return
        }
        for (server in all) {
            val row = fileesSettingsRow()
            val label = TextView(this)
            label.layoutParams = LinearLayout.LayoutParams(0, LinearLayout.LayoutParams.WRAP_CONTENT, 1f)
            label.setTextAppearance(R.style.TextAppearance_Filees_Name)
            label.maxLines = 2
            label.ellipsize = TextUtils.TruncateAt.END
            label.text = if (server.id == currentId) {
                "${server.label()} (${getString(R.string.server_current)})"
            } else {
                server.label()
            }
            label.setOnClickListener {
                FileesSession.select(prefs, server.id)
                finish()
            }
            val remove = MaterialButton(this, null, com.google.android.material.R.attr.materialButtonOutlinedStyle)
            remove.text = getString(R.string.action_unpair_short)
            styleFileesOutline(remove, R.color.filees_destructive)
            remove.setOnClickListener { confirmUnpair(server) }
            row.addView(label)
            row.addView(remove)
            binding.listServers.addView(row)
        }
    }

    private fun confirmUnpair(server: PairedServer) {
        val prefs = getSharedPreferences(FileesSession.PREFS, MODE_PRIVATE)
        AlertDialog.Builder(this)
            .setTitle(R.string.action_unpair_short)
            .setMessage(R.string.confirm_unpair)
            .setPositiveButton(R.string.action_unpair_short) { _, _ ->
                val wasCurrent = server.id == FileesSession.current(prefs)?.id
                DownloadCache.forget(cacheDir, server.id)
                FileesSession.unpairId(prefs, server.id)
                if (wasCurrent) {
                    finish()
                    return@setPositiveButton
                }
                bindServerDetails(prefs)
                renderServers()
                renderUploadTarget()
            }
            .setNegativeButton(R.string.action_cancel, null)
            .show()
    }

    // Upload target list is scoped to capturable shares: rw, not the realm
    // trash. A read-only grant cannot receive an upload; trash is a reject
    // waiting room, not a camera dump. Shelves stay writable.
    //
    // Retries with backoff before surfacing an error: on a real device this
    // fetch races a background FileesWatchTick send over an independent SSH
    // connection (sshtransport.Transport dials fresh per operation, so the
    // two never share a session slot - MaxSessions 1 on _filees-mobile does
    // not explain a collision here). What we actually saw live, 2026-09-26,
    // was "handshake failed: read tcp ... use of closed connection" - a
    // transient transport reset (cellular RRC/NAT reassignment, or the
    // background send saturating the uplink) rather than a server-side
    // rejection. A few short retries absorb exactly that kind of blip
    // instead of dead-ending the user on the first one.
    private fun loadUploadRepos(client: Client) {
        Thread {
            val backoffMs = longArrayOf(0L, 1500L, 3000L)
            var lastError: Exception? = null
            for (delay in backoffMs) {
                if (delay > 0) {
                    try {
                        Thread.sleep(delay)
                    } catch (_: InterruptedException) {
                        return@Thread
                    }
                }
                try {
                    val projection = RealmProjection.fromJson(client.listRepositoriesJSON())
                    val drawers = try {
                        DrawerFrame.fromJson(client.listDrawersJSON())
                    } catch (_: Exception) {
                        DrawerFrame.empty()
                    }
                    val capturable = projection.shares.filter { it.canCapture }
                    val prefs = getSharedPreferences(FileesSession.PREFS, MODE_PRIVATE)
                    watchUi {
                        FileesSession.rememberProjection(prefs, projection, watched.serverId)
                        uploadRepos = capturable
                        drawerFrame = drawers
                        uploadReposReady = true
                        uploadReposError = null
                        renderUploadTarget()
                        bindServerDetails(prefs)
                        renderServers()
                        resumePendingUploadTargetPicks()
                    }
                    return@Thread
                } catch (e: Exception) {
                    lastError = e
                }
            }
            watchUi {
                uploadRepos = emptyList()
                uploadReposReady = true
                uploadReposError = lastError?.message?.ifBlank { null } ?: getString(R.string.error_generic)
                resumePendingUploadTargetPicks()
            }
        }.start()
    }

    private fun resumePendingUploadTargetPicks() {
        val queued = pendingUploadTargetPicks.toList()
        pendingUploadTargetPicks.clear()
        queued.forEach { pickUploadTarget(it) }
    }

    private fun renderUploadTarget() {
        val prefs = getSharedPreferences(FileesSession.PREFS, MODE_PRIVATE)
        val name = prefs.getString(FileesSession.PREF_UPLOAD_REPO_NAME, null)
        binding.textUploadTarget.text = name ?: getString(R.string.upload_target_none)
    }

    private fun pickUploadTarget(onPicked: (() -> Unit)? = null) {
        if (!watchCurrent()) return
        if (!uploadReposReady) {
            // Queued, not dropped: loadUploadRepos's background thread calls
            // resumePendingUploadTargetPicks once it resolves (success or
            // failure, including after its internal retries), which replays
            // this exact call - the user does not have to dismiss this and
            // try again by hand. Cancel only forgets this one queued
            // callback; it does not stop loadUploadRepos itself, which keeps
            // running so a later pick does not have to start over.
            pendingUploadTargetPicks.add(onPicked)
            val view = layoutInflater.inflate(R.layout.dialog_loading, null)
            view.findViewById<TextView>(R.id.textLoadingMessage)
                .setText(R.string.upload_target_pick_loading)
            AlertDialog.Builder(this)
                .setTitle(R.string.upload_target_pick_title)
                .setView(view)
                .setNegativeButton(R.string.action_cancel) { _, _ ->
                    pendingUploadTargetPicks.remove(onPicked)
                }
                .show()
            return
        }
        val failure = uploadReposError
        if (failure != null) {
            AlertDialog.Builder(this)
                .setTitle(R.string.upload_target_pick_title)
                .setMessage(failure)
                .setPositiveButton(android.R.string.ok, null)
                .show()
            return
        }
        if (uploadRepos.isEmpty()) {
            AlertDialog.Builder(this)
                .setTitle(R.string.upload_target_pick_title)
                .setMessage(R.string.upload_target_pick_empty)
                .setPositiveButton(android.R.string.ok, null)
                .show()
            return
        }
        val names = uploadRepos.map { share ->
            val drawer = drawerFrame.nameFor(share.repoId)
            if (drawer.isBlank()) share.displayName else "$drawer — ${share.displayName}"
        }.toTypedArray()
        AlertDialog.Builder(this)
            .setTitle(R.string.upload_target_pick_title)
            .setItems(names) { _, index ->
                if (!watchCurrent()) return@setItems
                val chosen = uploadRepos[index]
                val request = ++watchChange
                confirmExistingBacklogThen(chosen.repoId, request) {
                    if (request == watchChange && watchCurrent()) {
                        // Do not expose the new destination to a worker until
                        // the operator has accepted its existing-file policy.
                        val applied = FileesSession.setUploadTarget(
                            getSharedPreferences(FileesSession.PREFS, MODE_PRIVATE),
                            chosen.repoId, chosen.displayName, watched.serverId,
                        )
                        if (applied) {
                            renderUploadTarget()
                            onPicked?.invoke()
                            FileesWatchScheduler.runSoon(this)
                        }
                    }
                }
            }
            .show()
    }

    // Guards a gap distinct from confirmAndAddWatch's per-folder prompt:
    // a target picked here can suddenly make an OLDER watch's already-
    // accumulated, never-confirmed backlog eligible for upload (e.g. a
    // watch added before this confirmation existed at all, or one added
    // while no target was set yet). Scans every currently watched folder
    // except whatever confirmAndAddWatch just handled on its own (that one
    // isn't added to `watched` until after this resolves - see
    // finishAddWatch's ordering) and asks the same three-way question
    // again, aggregated, before anything is allowed to send.
    private fun confirmExistingBacklogThen(repoId: String, request: Int, onPicked: (() -> Unit)?) {
        val trees = watched.uris()
        if (trees.isEmpty()) {
            onPicked?.invoke()
            return
        }
        Thread {
            val pending = try {
                trees.flatMap { DocumentWalk.tree(contentResolver, it) }
                    .filterNot { watched.alreadySeen(CaptureTransfers.source(it), repoId) }
            } catch (e: Exception) {
                watchUi { if (request == watchChange) showTransportError(getString(R.string.error_list), e, null) }
                return@Thread
            }
            watchUi {
                if (request != watchChange) return@watchUi
                if (pending.isEmpty()) {
                    onPicked?.invoke()
                    return@watchUi
                }
                showWatchDepthDialog(
                    getString(R.string.watch_confirm_title),
                    getString(R.string.watch_backlog_message, depthCount(pending)),
                    pending,
                ) { depth ->
                    if (watchCurrent() && request == watchChange) {
                        try {
                            markOutsideDepth(pending, depth, repoId)
                            onPicked?.invoke()
                        } catch (e: Exception) {
                            showTransportError(getString(R.string.error_send), e, null)
                        }
                    }
                }
            }
        }.start()
    }

    private fun confirmAndAddWatch(uri: Uri) {
        if (!watchCurrent()) return
        Thread {
            try {
                val files = DocumentWalk.tree(contentResolver, uri)
                watchUi { showWatchConfirmDialog(uri, files) }
            } catch (e: Exception) {
                watchUi { showTransportError(getString(R.string.error_list), e, null) }
            }
        }.start()
    }

    private fun showWatchConfirmDialog(uri: Uri, files: List<WalkedFile>) {
        if (files.isEmpty()) {
            finishAddWatch(uri, files, WatchDepth.ONLY_NEW)
            return
        }
        val summary = FolderPreflight.of(files)
        showWatchDepthDialog(
            getString(R.string.watch_confirm_title),
            getString(
                R.string.watch_confirm_message,
                treeLabel(uri),
                summary.files,
                HumanSize.format(summary.bytes),
            ),
            files,
        ) { depth -> finishAddWatch(uri, files, depth) }
    }

    private fun showWatchDepthDialog(
        title: String,
        message: String,
        files: List<WalkedFile>,
        onChosen: (WatchDepth) -> Unit,
    ) {
        val now = System.currentTimeMillis()
        val depths = WatchDepth.entries
        val labels = depths.map { depth ->
            val included = files.filter { depth.includes(it, now) }
            getString(R.string.watch_depth_item, depthLabel(depth), depthCount(included))
        }
        val view = layoutInflater.inflate(R.layout.dialog_watch_depth, null)
        view.findViewById<TextView>(R.id.textWatchDepthMessage).text = message
        val list = view.findViewById<LinearLayout>(R.id.listWatchDepth)
        val dialog = AlertDialog.Builder(this)
            .setTitle(title)
            .setView(view)
            .setNegativeButton(R.string.action_cancel, null)
            .create()
        labels.forEachIndexed { index, label ->
            val row = MaterialButton(this, null, com.google.android.material.R.attr.borderlessButtonStyle)
            row.text = label
            row.isAllCaps = false
            row.gravity = android.view.Gravity.START or android.view.Gravity.CENTER_VERTICAL
            row.layoutParams = LinearLayout.LayoutParams(
                LinearLayout.LayoutParams.MATCH_PARENT,
                LinearLayout.LayoutParams.WRAP_CONTENT,
            )
            row.setOnClickListener {
                dialog.dismiss()
                onChosen(depths[index])
            }
            list.addView(row)
        }
        dialog.show()
    }

    private fun treeLabel(uri: Uri): String {
        val name = androidx.documentfile.provider.DocumentFile.fromTreeUri(this, uri)?.name
        if (!name.isNullOrBlank()) return name
        return uri.lastPathSegment ?: uri.toString()
    }

    private fun depthLabel(depth: WatchDepth): String = when (depth) {
        WatchDepth.ONLY_NEW -> getString(R.string.watch_depth_new)
        WatchDepth.DAY -> getString(R.string.watch_depth_day)
        WatchDepth.WEEK -> getString(R.string.watch_depth_week)
        WatchDepth.MONTH -> getString(R.string.watch_depth_month)
        WatchDepth.YEAR -> getString(R.string.watch_depth_year)
        WatchDepth.ALL -> getString(R.string.watch_depth_all)
    }

    private fun depthCount(files: List<WalkedFile>): String {
        if (files.isEmpty()) return getString(R.string.watch_depth_none)
        val summary = FolderPreflight.of(files)
        return resources.getQuantityString(
            R.plurals.status_preflight, summary.files, summary.files, HumanSize.format(summary.bytes),
        )
    }

    private fun markOutsideDepth(files: List<WalkedFile>, depth: WatchDepth, repoId: String) {
        val now = System.currentTimeMillis()
        files.filterNot { depth.includes(it, now) }
            .forEach { watched.markSeen(CaptureTransfers.source(it), repoId) }
    }

    // Files outside the chosen depth are marked seen, so the next tick
    // sends only the rest. Later arrivals are not in this list and still go.
    //
    // Deliberately picks the upload target (when unset) BEFORE calling
    // watched.add(uri): confirmExistingBacklogThen's scan reads
    // watched.uris(), so doing it in this order means that scan never sees
    // (and never re-asks about) the very folder this dialog just finished
    // confirming on its own.
    private fun finishAddWatch(uri: Uri, files: List<WalkedFile>, depth: WatchDepth) {
        if (!watchCurrent()) return
        try {
            contentResolver.takePersistableUriPermission(uri, Intent.FLAG_GRANT_READ_URI_PERMISSION)
        } catch (e: Exception) {
            showTransportError(getString(R.string.error_list), e, null)
            return
        }
        val addThisWatch = {
            val server = FileesSession.current(getSharedPreferences(FileesSession.PREFS, MODE_PRIVATE))
            val repoId = server?.uploadRepoId.orEmpty()
            if (server?.id == watched.serverId && watchCurrent() && repoId.isNotBlank()) {
                // Persist exclusions BEFORE making the tree visible to workers.
                try {
                    markOutsideDepth(files, depth, repoId)
                    watched.add(uri)
                    renderWatched()
                    FileesWatchScheduler.runSoon(this)
                    if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU &&
                        ContextCompat.checkSelfPermission(this, Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED
                    ) {
                        notificationPermissionLauncher.launch(Manifest.permission.POST_NOTIFICATIONS)
                    }
                } catch (e: Exception) {
                    showTransportError(getString(R.string.error_send), e, null)
                }
            }
        }
        if (getSharedPreferences(FileesSession.PREFS, MODE_PRIVATE).getString(FileesSession.PREF_UPLOAD_REPO_ID, null).isNullOrBlank()) {
            pickUploadTarget(addThisWatch)
        } else {
            addThisWatch()
        }
    }

    private fun renderWatched() {
        binding.listWatched.removeAllViews()
        watchLabels.clear()
        binding.listWatched.addView(fileesMetaText(getString(R.string.watch_server_scope,
            FileesSession.serverLabel(getSharedPreferences(FileesSession.PREFS, MODE_PRIVATE)))))
        val uris = watched.uris()
        val unassigned = watched.unassigned()
        if (unassigned.isNotEmpty()) {
            binding.listWatched.addView(fileesMetaText(getString(R.string.watch_unassigned)))
            for (uri in unassigned) {
                val row = fileesSettingsRow().apply { orientation = LinearLayout.VERTICAL }
                val label = fileesMetaText(uri.lastPathSegment ?: uri.toString()).apply {
                    layoutParams = LinearLayout.LayoutParams(LinearLayout.LayoutParams.MATCH_PARENT, LinearLayout.LayoutParams.WRAP_CONTENT)
                }
                val bind = MaterialButton(this, null, com.google.android.material.R.attr.materialButtonOutlinedStyle)
                bind.text = getString(R.string.action_add_watched)
                styleFileesOutline(bind, R.color.filees_accent_text)
                bind.isEnabled = !watched.serverId.isNullOrBlank()
                bind.setOnClickListener { confirmAndAddWatch(uri) }
                val remove = MaterialButton(this, null, com.google.android.material.R.attr.materialButtonOutlinedStyle)
                remove.text = getString(R.string.action_remove_watched)
                styleFileesOutline(remove, R.color.filees_destructive)
                remove.setOnClickListener { watched.removeUnassigned(uri); renderWatched() }
                val actions = fileesSettingsRow()
                actions.addView(bind); actions.addView(remove)
                row.addView(label); row.addView(actions)
                binding.listWatched.addView(row)
            }
        }
        if (uris.isEmpty()) {
            binding.listWatched.addView(fileesMetaText(getString(R.string.watched_empty)))
            return
        }
        for (uri in uris) {
            val row = fileesSettingsRow()
            val label = TextView(this)
            label.layoutParams = LinearLayout.LayoutParams(0, LinearLayout.LayoutParams.WRAP_CONTENT, 1f)
            label.setTextAppearance(R.style.TextAppearance_Filees_Name)
            label.maxLines = 2
            label.ellipsize = TextUtils.TruncateAt.END
            label.text = uri.lastPathSegment ?: uri.toString()
            val remove = MaterialButton(this, null, com.google.android.material.R.attr.materialButtonOutlinedStyle)
            remove.text = getString(R.string.action_remove_watched)
            styleFileesOutline(remove, R.color.filees_destructive)
            remove.setOnClickListener {
                watched.remove(uri)
                renderWatched()
            }
            val labels = LinearLayout(this).apply {
                orientation = LinearLayout.VERTICAL
                layoutParams = LinearLayout.LayoutParams(0,LinearLayout.LayoutParams.WRAP_CONTENT,1f)
            }
            label.layoutParams = LinearLayout.LayoutParams(LinearLayout.LayoutParams.MATCH_PARENT,LinearLayout.LayoutParams.WRAP_CONTENT)
            val status = fileesMetaText("")
            watchLabels[uri] = status
            labels.addView(label); labels.addView(status)
            labels.setOnClickListener { WatchFolderCard.show(this,uri) }
            row.addView(labels)
            row.addView(remove)
            binding.listWatched.addView(row)
        }
        refreshWatchLabels()
    }

    private fun fileesSettingsRow(): LinearLayout {
        val row = LinearLayout(this)
        row.orientation = LinearLayout.HORIZONTAL
        row.gravity = android.view.Gravity.CENTER_VERTICAL
        row.minimumHeight = resources.getDimensionPixelSize(R.dimen.filees_row_min)
        return row
    }

    private fun fileesMetaText(value: String): TextView {
        val view = TextView(this)
        view.setTextAppearance(R.style.TextAppearance_Filees_Meta)
        view.text = value
        return view
    }

    private fun styleFileesOutline(button: MaterialButton, colorRes: Int) {
        val color = ContextCompat.getColor(this, colorRes)
        button.setTextColor(color)
        button.strokeColor = ColorStateList.valueOf(color)
        button.rippleColor = ColorStateList.valueOf(color)
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
                FileesSession.putAndSelect(
                    getSharedPreferences(FileesSession.PREFS, MODE_PRIVATE),
                    json.getString("address"),
                    json.getString("host_public_key"),
                )
                uiSafe { finish() }
            } catch (_: Exception) {
                // Main screen shows connection errors on resume.
            }
        }.start()
    }
}
