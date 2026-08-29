package net.filees.mobile

import android.Manifest
import android.content.Intent
import android.content.pm.PackageManager
import android.net.Uri
import android.os.Build
import android.os.Bundle
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

        val prefs = getSharedPreferences(FileesSession.PREFS, MODE_PRIVATE)
        val address = prefs.getString(FileesSession.PREF_ADDRESS, null)
        val hostKey = prefs.getString(FileesSession.PREF_HOST_KEY, null)
        if (!address.isNullOrBlank() && !hostKey.isNullOrBlank()) {
            try {
                val client = Androidbind.newClient(filesDir.absolutePath, address, FileesSession.MOBILE_USER, hostKey)
                binding.textDevicePublicKey.text = client.publicKey()
                loadUploadRepos(client)
            } catch (_: Exception) {
                binding.textDevicePublicKey.text = getString(R.string.label_device_public_key)
            }
        }
        binding.textDetails.text = when {
            !address.isNullOrBlank() -> getString(R.string.settings_server, address)
            else -> prefs.getString(FileesSession.PREF_DETAILS, "")
        }
        renderWatched()
        renderUploadTarget()
    }

    // Upload target list is scoped to "rw" shares only - a read-only grant
    // cannot receive an upload, listing it would just be a dead end.
    private fun loadUploadRepos(client: Client) {
        Thread {
            try {
                val projection = RealmProjection.fromJson(client.listRepositoriesJSON())
                uploadRepos = projection.shares.filter { it.selectable && it.access == "rw" }
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
                getSharedPreferences(FileesSession.PREFS, MODE_PRIVATE).edit {
                    putString(FileesSession.PREF_UPLOAD_REPO_ID, chosen.repoId)
                    putString(FileesSession.PREF_UPLOAD_REPO_NAME, chosen.displayName)
                }
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
