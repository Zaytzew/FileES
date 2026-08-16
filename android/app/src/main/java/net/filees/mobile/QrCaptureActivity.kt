package net.filees.mobile

import com.journeyapps.barcodescanner.CaptureActivity
import com.journeyapps.barcodescanner.DecoratedBarcodeView

/**
 * Portrait, square-framed capture. The library CaptureActivity is
 * sensorLandscape with a wide laser viewfinder — that is a 1D barcode
 * scanner, and it will not reliably read the pairing QR from a screen.
 */
class QrCaptureActivity : CaptureActivity() {
    override fun initializeContent(): DecoratedBarcodeView {
        setContentView(R.layout.activity_qr_capture)
        return findViewById(R.id.zxing_barcode_scanner)
    }
}
