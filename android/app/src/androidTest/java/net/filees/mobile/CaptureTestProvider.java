package net.filees.mobile;

import android.content.ContentProvider;
import android.content.ContentValues;
import android.database.Cursor;
import android.net.Uri;
import android.os.ParcelFileDescriptor;
import java.io.FileNotFoundException;
import java.io.IOException;

/** Test APK only. Java keeps the provider independent of the target APK runtime. */
public class CaptureTestProvider extends ContentProvider {
    public boolean onCreate() { return true; }
    public String getType(Uri uri) { return "application/octet-stream"; }
    public Cursor query(Uri uri, String[] p, String s, String[] a, String o) { return null; }
    public Uri insert(Uri uri, ContentValues v) { return null; }
    public int delete(Uri uri, String s, String[] a) { return 0; }
    public int update(Uri uri, ContentValues v, String s, String[] a) { return 0; }
    public ParcelFileDescriptor openFile(Uri uri, String mode) throws FileNotFoundException {
        if ("missing".equals(uri.getLastPathSegment())) throw new FileNotFoundException("test source unavailable");
        final long count = Long.parseLong(uri.getLastPathSegment());
        try {
            final ParcelFileDescriptor[] pipe = ParcelFileDescriptor.createPipe();
            new Thread(() -> {
                try (ParcelFileDescriptor.AutoCloseOutputStream out = new ParcelFileDescriptor.AutoCloseOutputStream(pipe[1])) {
                    byte[] bytes = new byte[64 * 1024];
                    for(int i=0;i<bytes.length;i++) bytes[i]=(byte)(i%251);
                    long left=count;
                    while(left>0) { int n=(int)Math.min(left,bytes.length); out.write(bytes,0,n); left-=n; }
                } catch(IOException ignored) { /* reader cancelled */ }
            }).start();
            return pipe[0];
        } catch(IOException e) { throw new FileNotFoundException(e.toString()); }
    }
}
