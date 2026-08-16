package net.filees.mobile

import android.app.Activity
import android.view.View
import android.view.ViewGroup
import androidx.core.content.ContextCompat
import androidx.core.view.ViewCompat
import androidx.core.view.WindowCompat
import androidx.core.view.WindowInsetsCompat
import androidx.core.view.WindowInsetsControllerCompat
import androidx.core.view.updateLayoutParams
import androidx.core.view.updatePadding

fun Activity.installFileesWindow() {
    WindowCompat.setDecorFitsSystemWindows(window, false)
    val navy = ContextCompat.getColor(this, R.color.filees_navy)
    @Suppress("DEPRECATION")
    run {
        window.statusBarColor = navy
        window.navigationBarColor = navy
    }
    WindowInsetsControllerCompat(window, window.decorView).apply {
        isAppearanceLightStatusBars = false
        isAppearanceLightNavigationBars = false
    }
}

fun View.padTopSystemBars() {
    val start = paddingStart
    val end = paddingEnd
    val bottom = paddingBottom
    ViewCompat.setOnApplyWindowInsetsListener(this) { view, insets ->
        val bars = insets.getInsets(
            WindowInsetsCompat.Type.systemBars() or WindowInsetsCompat.Type.displayCutout(),
        )
        view.updatePadding(
            left = start + bars.left,
            top = bars.top,
            right = end + bars.right,
            bottom = bottom,
        )
        insets
    }
    requestApplyInsetsWhenAttached()
}

fun View.marginBottomSystemBars(extraDp: Int = 0) {
    val extraPx = dp(extraDp)
    ViewCompat.setOnApplyWindowInsetsListener(this) { view, insets ->
        val bars = insets.getInsets(
            WindowInsetsCompat.Type.systemBars() or WindowInsetsCompat.Type.displayCutout(),
        )
        view.updateLayoutParams<ViewGroup.MarginLayoutParams> {
            bottomMargin = bars.bottom + extraPx
        }
        insets
    }
    requestApplyInsetsWhenAttached()
}

fun View.padBottomSystemBars(extraDp: Int = 0) {
    val start = paddingStart
    val top = paddingTop
    val end = paddingEnd
    val extraPx = dp(extraDp)
    ViewCompat.setOnApplyWindowInsetsListener(this) { view, insets ->
        val bars = insets.getInsets(
            WindowInsetsCompat.Type.systemBars() or WindowInsetsCompat.Type.displayCutout(),
        )
        view.updatePadding(
            left = start + bars.left,
            top = top,
            right = end + bars.right,
            bottom = bars.bottom + extraPx,
        )
        insets
    }
    requestApplyInsetsWhenAttached()
}

private fun View.dp(value: Int): Int =
    (value * resources.displayMetrics.density).toInt()

private fun View.requestApplyInsetsWhenAttached() {
    if (isAttachedToWindow) {
        ViewCompat.requestApplyInsets(this)
    } else {
        addOnAttachStateChangeListener(object : View.OnAttachStateChangeListener {
            override fun onViewAttachedToWindow(v: View) {
                v.removeOnAttachStateChangeListener(this)
                ViewCompat.requestApplyInsets(v)
            }

            override fun onViewDetachedFromWindow(v: View) = Unit
        })
    }
}
