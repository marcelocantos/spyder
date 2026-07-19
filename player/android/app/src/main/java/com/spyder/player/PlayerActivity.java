package com.spyder.player;

import android.content.Intent;
import android.graphics.Insets;
import android.os.Bundle;
import android.os.Looper;
import android.view.View;
import android.view.WindowInsets;
import android.view.WindowInsetsController;
import org.libsdl.app.SDLActivity;

import java.util.concurrent.CountDownLatch;
import java.util.concurrent.TimeUnit;

public class PlayerActivity extends SDLActivity {
    // Holds the stream_addr (or legacy stream_addr) intent extra so native code
    // can retrieve it via JNI. Set before SDL's native thread starts; cleared
    // after first read.
    private static volatile String sStreamAddr = null;
    // Optional server catalogue name (default native: "tiltbuggy").
    private static volatile String sServerName = null;

    // Display-cutout-only insets {left, right, top, bottom} px — drawSafe.
    // Pixel with no punch-hole stays {0,0,0,0} so drawSafe == full surface.
    private volatile int[] cutoutInsets = new int[]{0, 0, 0, 0};

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        // Read intent extra before super.onCreate() loads native libraries
        // and starts the SDL thread — available when SDL_main runs.
        Intent intent = getIntent();
        if (intent != null) {
            String addr = intent.getStringExtra("stream_addr");
            if (addr == null || addr.isEmpty()) {
                addr = intent.getStringExtra("stream_addr"); // legacy alias
            }
            if (addr != null && !addr.isEmpty()) {
                sStreamAddr = addr;
            }
            String name = intent.getStringExtra("server_name");
            if (name != null && !name.isEmpty()) {
                sServerName = name;
            }
        }
        super.onCreate(savedInstanceState);
        getWindow().getDecorView().setOnApplyWindowInsetsListener((v, ins) -> {
            Insets c = ins.getInsets(WindowInsets.Type.displayCutout());
            cutoutInsets = new int[]{c.left, c.right, c.top, c.bottom};
            return ins;
        });
    }

    /** Cutouts only (not status/nav/gesture). Native: CutoutInsets_android.cpp. */
    public int[] getDisplayCutoutInsets() { return cutoutInsets; }

    // Called from native (JNI) to retrieve the intent-supplied relay address.
    // Returns e.g. "192.168.1.100:3030" or null if absent.
    // Clears after first read so it does not persist across Activity restarts.
    public static String getStreamAddr() {
        String addr = sStreamAddr;
        sStreamAddr = null;
        return addr;
    }

    /** Optional catalogue name for /ws/wire?preference=… (null → native default). */
    public static String getServerName() {
        String name = sServerName;
        sServerName = null;
        return name;
    }

    /** @deprecated Use {@link #getStreamAddr()}; kept for any old native stubs. */
    public static String getGedAddr() {
        return getStreamAddr();
    }

    /**
     * 🎯T154: apply SessionConfig.immersive on the stream viewer.
     * Part of SessionConfig application — runs before DeviceInfo is measured.
     * Blocks until bars are hidden/shown and a layout/insets pass has run so
     * the first surface snapshot is already the configured one.
     */
    public void applyImmersive(final boolean enabled) {
        final CountDownLatch done = new CountDownLatch(1);
        final Runnable apply = () -> {
            try {
                getWindow().setDecorFitsSystemWindows(!enabled);
                WindowInsetsController c = getWindow().getInsetsController();
                if (c != null) {
                    if (enabled) {
                        c.hide(WindowInsets.Type.systemBars());
                        c.setSystemBarsBehavior(
                            WindowInsetsController.BEHAVIOR_SHOW_TRANSIENT_BARS_BY_SWIPE);
                    } else {
                        c.show(WindowInsets.Type.systemBars());
                    }
                }
                final View decor = getWindow().getDecorView();
                decor.requestApplyInsets();
                decor.requestLayout();
                // Two posts: after the next layout, insets are readable.
                decor.post(() -> decor.post(done::countDown));
            } catch (Throwable t) {
                done.countDown();
            }
        };
        if (Looper.myLooper() == Looper.getMainLooper()) {
            apply.run();
        } else {
            runOnUiThread(apply);
        }
        try {
            done.await(2, TimeUnit.SECONDS);
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
        }
    }

    @Override
    protected String[] getLibraries() {
        return new String[]{"SDL3", "main"};
    }
}
