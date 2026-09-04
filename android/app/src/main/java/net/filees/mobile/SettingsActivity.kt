package net.filees.mobile

import android.Manifest
import android.content.Intent
import android.content.pm.PackageManager
import android.net.Uri
import android.os.Build
import android.os.Bundle
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
        binding.buttonChangeUploadTarget.setOnClickListener { pickUploadTarget() }
        binding.buttonUnpair.setOnClickListener { confirmUnpair() }

        val prefs = getSharedPreferences(FileesSession.PREFS, MODE_PRIVATE)
        FileesSession.migrate(prefs)
        val address = prefs.getString(FileesSession.PREF_ADDRESS, null)
        val hostKey = prefs.getString(FileesSession.PREF_HOST_KEY, null)
        if (!address.isNullOrBlank() && !hostKey.isNullOrBlank()) {
            try {
                val client = Androidbind.newClient(
                    filesDir.absolutePath,
                    DialAddress.resolve(address),
                    FileesSession.MOBILE_USER,
                    hostKey,
                )
                binding.textDevicePublicKey.text = client.publicKey()
                loadUploadRepos(client)
            } catch (_: Exception) {
                binding.textDevicePublicKey.text = getString(R.string.label_device_public_key)
            }
        }
        bindServerDetails(prefs)
        binding.buttonUnpair.visibility = if (FileesSession.current(prefs) == null) View.GONE else View.VISIBLE
        renderServers()
        renderWatched()
        renderUploadTarget()
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
            val empty = TextView(this)
            empty.text = getString(R.string.status_idle)
            binding.listServers.addView(empty)
            return
        }
        val accent = ContextCompat.getColor(this, R.color.filees_orange)
        for (server in all) {
            val row = LinearLayout(this)
            row.orientation = LinearLayout.HORIZONTAL
            val label = TextView(this)
            label.layoutParams = LinearLayout.LayoutParams(0, LinearLayout.LayoutParams.WRAP_CONTENT, 1f)
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
            remove.text = getString(R.string.action_unpair)
            remove.setTextColor(accent)
            remove.strokeColor = android.content.res.ColorStateList.valueOf(accent)
            remove.rippleColor = android.content.res.ColorStateList.valueOf(accent)
            remove.setOnClickListener {
                val wasCurrent = server.id == currentId
                FileesSession.unpairId(prefs, server.id)
                if (wasCurrent) {
                    finish()
                    return@setOnClickListener
                }
                bindServerDetails(prefs)
                binding.buttonUnpair.visibility =
                    if (FileesSession.current(prefs) == null) View.GONE else View.VISIBLE
                renderServers()
                renderUploadTarget()
            }
            row.addView(label)
            row.addView(remove)
            binding.listServers.addView(row)
        }
    }

    private fun confirmUnpair() {
        AlertDialog.Builder(this)
            .setTitle(R.string.action_unpair)
            .setMessage(R.string.confirm_unpair)
            .setPositiveButton(R.string.action_unpair) { _, _ ->
                FileesSession.unpair(getSharedPreferences(FileesSession.PREFS, MODE_PRIVATE))
                finish()
            }
            .setNegativeButton(R.string.action_cancel, null)
            .show()
    }

    // Upload target list is scoped to capturable shares: rw, not the realm
    // trash. A read-only grant cannot receive an upload; trash is a reject
    // waiting room, not a camera dump. Shelves stay writable.
    private fun loadUploadRepos(client: Client) {
        Thread {
            try {
                val projection = RealmProjection.fromJson(client.listRepositoriesJSON())
                val capturable = projection.shares.filter { it.canCapture }
                val prefs = getSharedPreferences(FileesSession.PREFS, MODE_PRIVATE)
                FileesSession.rememberProjection(prefs, projection)
                runOnUiThread {
                    uploadRepos = capturable
                    renderUploadTarget()
                    bindServerDetails(prefs)
                    renderServers()
                }
            } catch (_: Exception) {
                // Settings still work without this list; picking a target
                // just fails over to upload_target_pick_empty until the
                // next visit succeeds.
            }
        }.start()
    }

    private fun renderUploadTarget() {
        val prefs = getSharedPreferences(FileesSession.PREFS, MODE_PRIVATE)
        val name = prefs.getString(FileesSession.PREF_UPLOAD_REPO_NAME, null)
        binding.textUploadTarget.text = name ?: getString(R.string.upload_target_none)
    }

    private fun pickUploadTarget(onPicked: (() -> Unit)? = null) {
        if (uploadRepos.isEmpty()) {
            AlertDialog.Builder(this)
                .setTitle(R.string.upload_target_pick_title)
                .setMessage(R.string.upload_target_pick_empty)
                .setPositiveButton(android.R.string.ok, null)
                .show()
            return
        }
        val names = uploadRepos.map { it.displayName }.toTypedArray()
        AlertDialog.Builder(this)
            .setTitle(R.string.upload_target_pick_title)
            .setItems(names) { _, index ->
                val chosen = uploadRepos[index]
                FileesSession.setUploadTarget(
                    getSharedPreferences(FileesSession.PREFS, MODE_PRIVATE),
                    chosen.repoId,
                    chosen.displayName,
                )
                renderUploadTarget()
                confirmExistingBacklogThen(onPicked)
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
    private fun confirmExistingBacklogThen(onPicked: (() -> Unit)?) {
        val trees = watched.uris()
        if (trees.isEmpty()) {
            onPicked?.invoke()
            return
        }
        Thread {
            val pending = trees
                .flatMap { DocumentWalk.tree(contentResolver, it) }
                .filterNot { watched.alreadySeen(it.uri.toString() + "/" + it.filename) }
            runOnUiThread {
                if (pending.isEmpty()) {
                    onPicked?.invoke()
                    return@runOnUiThread
                }
                val summary = FolderPreflight.of(pending)
                val countText = resources.getQuantityString(
                    R.plurals.status_preflight, summary.files, summary.files, HumanSize.format(summary.bytes),
                )
                AlertDialog.Builder(this)
                    .setTitle(R.string.watch_confirm_title)
                    .setMessage(getString(R.string.watch_backlog_message, countText))
                    .setPositiveButton(R.string.action_watch_send_existing) { _, _ -> onPicked?.invoke() }
                    .setNeutralButton(R.string.action_watch_only_new) { _, _ ->
                        pending.forEach { watched.markSeen(it.uri.toString() + "/" + it.filename) }
                        onPicked?.invoke()
                    }
                    .setNegativeButton(R.string.action_cancel, null)
                    .show()
            }
        }.start()
    }

    private fun confirmAndAddWatch(uri: Uri) {
        Thread {
            val files = DocumentWalk.tree(contentResolver, uri)
            runOnUiThread { showWatchConfirmDialog(uri, files) }
        }.start()
    }

    private fun showWatchConfirmDialog(uri: Uri, files: List<WalkedFile>) {
        val summary = FolderPreflight.of(files)
        val countText = resources.getQuantityString(
            R.plurals.status_preflight, summary.files, summary.files, HumanSize.format(summary.bytes),
        )
        val name = uri.lastPathSegment ?: uri.toString()
        AlertDialog.Builder(this)
            .setTitle(R.string.watch_confirm_title)
            .setMessage(getString(R.string.watch_confirm_message, name, countText))
            .setPositiveButton(R.string.action_watch_send_existing) { _, _ -> finishAddWatch(uri, files, sendExisting = true) }
            .setNeutralButton(R.string.action_watch_only_new) { _, _ -> finishAddWatch(uri, files, sendExisting = false) }
            .setNegativeButton(R.string.action_cancel, null)
            .show()
    }

    // sendExisting=false marks every file already in the folder as seen
    // right away, so FileesWatchTick's next run treats the whole existing
    // backlog as already handled and only picks up files that appear from
    // here on - the "tylko nowe od teraz" choice.
    //
    // Deliberately picks the upload target (when unset) BEFORE calling
    // watched.add(uri): confirmExistingBacklogThen's scan reads
    // watched.uris(), so doing it in this order means that scan never sees
    // (and never re-asks about) the very folder this dialog just finished
    // confirming on its own.
    private fun finishAddWatch(uri: Uri, files: List<WalkedFile>, sendExisting: Boolean) {
        contentResolver.takePersistableUriPermission(uri, Intent.FLAG_GRANT_READ_URI_PERMISSION)
        val addThisWatch = {
            watched.add(uri)
            if (!sendExisting) {
                files.forEach { watched.markSeen(it.uri.toString() + "/" + it.filename) }
            }
            renderWatched()
            FileesWatchScheduler.runSoon(this)
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU &&
                ContextCompat.checkSelfPermission(this, Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED
            ) {
                notificationPermissionLauncher.launch(Manifest.permission.POST_NOTIFICATIONS)
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
            // Default outlined-button style uses ?attr/colorPrimary (brand
            // navy) for text/stroke - invisible against this app's dark
            // navy background. See Widget.Filees.Button.OutlinedButton in
            // themes.xml for the XML-side fix; this one is built in code.
            val accent = ContextCompat.getColor(this, R.color.filees_orange)
            remove.setTextColor(accent)
            remove.strokeColor = android.content.res.ColorStateList.valueOf(accent)
            remove.rippleColor = android.content.res.ColorStateList.valueOf(accent)
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
                FileesSession.putAndSelect(
                    getSharedPreferences(FileesSession.PREFS, MODE_PRIVATE),
                    json.getString("address"),
                    json.getString("host_public_key"),
                )
                runOnUiThread { finish() }
            } catch (_: Exception) {
                // Main screen shows connection errors on resume.
            }
        }.start()
    }
}
