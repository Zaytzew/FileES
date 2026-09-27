package net.filees.mobile

import android.app.Application
import android.app.KeyguardManager
import android.os.Build
import android.os.SystemClock
import android.view.View
import android.view.ViewGroup
import android.view.WindowManager
import androidx.activity.OnBackPressedCallback
import androidx.biometric.BiometricManager.Authenticators.BIOMETRIC_STRONG
import androidx.biometric.BiometricManager.Authenticators.DEVICE_CREDENTIAL
import androidx.biometric.BiometricPrompt
import androidx.core.content.ContextCompat
import androidx.fragment.app.FragmentActivity
import androidx.lifecycle.DefaultLifecycleObserver
import androidx.lifecycle.LifecycleOwner
import androidx.lifecycle.ProcessLifecycleOwner

/**
 * Device-level gate in front of the UI. It uses the phone lock (strong
 * biometric or PIN/pattern/password) and does not stand in for FileES
 * pairing, SSH, or realm membership. Background uploads keep running.
 */
object FileesDeviceGate {

    // Long enough that the system folder picker or a recents hop does not
    // immediately ask again; short enough that a left-open phone is locked.
    private const val GRACE_MS = 30_000L

    private var unlocked = false
    private var stoppedAt = 0L
    private var suppressAuto = false
    private var promptInFlight = false

    fun install(app: Application) {
        ProcessLifecycleOwner.get().lifecycle.addObserver(object : DefaultLifecycleObserver {
            override fun onStop(owner: LifecycleOwner) {
                stoppedAt = SystemClock.elapsedRealtime()
            }

            override fun onStart(owner: LifecycleOwner) {
                if (stoppedAt == 0L) return
                if (SystemClock.elapsedRealtime() - stoppedAt > GRACE_MS) {
                    unlocked = false
                    suppressAuto = false
                }
            }
        })
        app.registerActivityLifecycleCallbacks(object : Application.ActivityLifecycleCallbacks {
            override fun onActivityResumed(activity: android.app.Activity) {
                val host = activity as? FragmentActivity ?: return
                onResume(host)
            }

            override fun onActivityCreated(activity: android.app.Activity, savedInstanceState: android.os.Bundle?) = Unit
            override fun onActivityStarted(activity: android.app.Activity) = Unit
            override fun onActivityPaused(activity: android.app.Activity) = Unit
            override fun onActivityStopped(activity: android.app.Activity) = Unit
            override fun onActivitySaveInstanceState(activity: android.app.Activity, outState: android.os.Bundle) = Unit
            override fun onActivityDestroyed(activity: android.app.Activity) {
                if (activity is FragmentActivity) promptInFlight = false
            }
        })
    }

    private fun onResume(activity: FragmentActivity) {
        if (activity.isFinishing) return
        if (!hasDeviceLock(activity) || unlocked) {
            hideOverlay(activity)
            return
        }
        showOverlay(activity)
        if (!suppressAuto && !promptInFlight) prompt(activity)
    }

    private fun hasDeviceLock(activity: FragmentActivity): Boolean {
        val km = activity.getSystemService(KeyguardManager::class.java) ?: return false
        return km.isDeviceSecure
    }

    private fun prompt(activity: FragmentActivity) {
        if (promptInFlight || unlocked || !hasDeviceLock(activity)) return
        promptInFlight = true
        val prompt = BiometricPrompt(
            activity,
            ContextCompat.getMainExecutor(activity),
            object : BiometricPrompt.AuthenticationCallback() {
                override fun onAuthenticationSucceeded(result: BiometricPrompt.AuthenticationResult) {
                    promptInFlight = false
                    suppressAuto = false
                    unlocked = true
                    hideOverlay(activity)
                }

                override fun onAuthenticationError(errorCode: Int, errString: CharSequence) {
                    promptInFlight = false
                    suppressAuto = true
                    if (!activity.isFinishing) showOverlay(activity)
                }

                override fun onAuthenticationFailed() {
                    // The system sheet stays up and retries; do not unlock.
                }
            },
        )
        try {
            prompt.authenticate(promptInfo(activity))
        } catch (_: Exception) {
            promptInFlight = false
            suppressAuto = true
        }
    }

    private fun promptInfo(activity: FragmentActivity): BiometricPrompt.PromptInfo {
        val builder = BiometricPrompt.PromptInfo.Builder()
            .setTitle(activity.getString(R.string.device_lock_prompt_title))
            .setConfirmationRequired(false)
        if (Build.VERSION.SDK_INT >= 30) {
            builder.setAllowedAuthenticators(BIOMETRIC_STRONG or DEVICE_CREDENTIAL)
        } else {
            @Suppress("DEPRECATION")
            builder.setDeviceCredentialAllowed(true)
        }
        return builder.build()
    }

    private fun showOverlay(activity: FragmentActivity) {
        activity.window.addFlags(WindowManager.LayoutParams.FLAG_SECURE)
        val overlay = ensureOverlay(activity)
        overlay.visibility = View.VISIBLE
        overlay.findViewById<View>(R.id.buttonDeviceUnlock).setOnClickListener {
            suppressAuto = false
            prompt(activity)
        }
        backCallback(activity).isEnabled = true
    }

    private fun hideOverlay(activity: FragmentActivity) {
        activity.window.clearFlags(WindowManager.LayoutParams.FLAG_SECURE)
        activity.findViewById<View>(R.id.device_lock_overlay)?.visibility = View.GONE
        (activity.window.decorView.getTag(R.id.device_lock_back) as? OnBackPressedCallback)?.isEnabled = false
    }

    private fun ensureOverlay(activity: FragmentActivity): View {
        val content = activity.findViewById<ViewGroup>(android.R.id.content)
        val existing = content.findViewById<View>(R.id.device_lock_overlay)
        if (existing != null) return existing
        val overlay = activity.layoutInflater.inflate(R.layout.overlay_device_lock, content, false)
        overlay.id = R.id.device_lock_overlay
        content.addView(overlay)
        return overlay
    }

    private fun backCallback(activity: FragmentActivity): OnBackPressedCallback {
        val existing = activity.window.decorView.getTag(R.id.device_lock_back) as? OnBackPressedCallback
        if (existing != null) return existing
        val callback = object : OnBackPressedCallback(true) {
            override fun handleOnBackPressed() {
                activity.moveTaskToBack(true)
            }
        }
        activity.onBackPressedDispatcher.addCallback(activity, callback)
        activity.window.decorView.setTag(R.id.device_lock_back, callback)
        return callback
    }
}
