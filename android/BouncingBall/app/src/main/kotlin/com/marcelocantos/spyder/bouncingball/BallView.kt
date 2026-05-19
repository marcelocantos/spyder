// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package com.marcelocantos.spyder.bouncingball

import android.content.Context
import android.graphics.Canvas
import android.graphics.Color
import android.graphics.Paint
import android.util.Log
import android.view.View

// BallView is a test fixture for spyder's Android log-capture path.
// It animates a single yellow ball bouncing inside the view bounds
// and emits a Log.i entry on every wall bounce — several per second,
// indefinitely. The recognisable signature makes it trivial to write
// a logcat-filter regression test:
//
//   BouncingBall: bounce wall=<top|bottom|left|right> count=<n> x=<f> y=<f>
//
// Tag is "BouncingBall" so callers can filter with
// `adb logcat -s BouncingBall:I`.
class BallView(context: Context) : View(context) {
    companion object { private const val TAG = "BouncingBall" }

    private val radius = 60f
    private var x = 200f
    private var y = 200f
    private var vx = 420f // pixels / second
    private var vy = 360f
    private var bounceCount = 0
    private var lastNanos: Long = 0

    private val ballPaint = Paint(Paint.ANTI_ALIAS_FLAG).apply {
        color = Color.parseColor("#FFD60A")
        style = Paint.Style.FILL
    }
    private val highlightPaint = Paint(Paint.ANTI_ALIAS_FLAG).apply {
        color = Color.parseColor("#FFEB78")
        style = Paint.Style.FILL
        alpha = 153 // ~0.6
    }
    private val textPaint = Paint(Paint.ANTI_ALIAS_FLAG).apply {
        color = Color.parseColor("#88FFFFFF")
        textSize = 36f
    }

    init {
        setBackgroundColor(Color.BLACK)
        Log.i(TAG, "BallView created — initial pos=$x,$y vel=$vx,$vy")
    }

    override fun onDraw(canvas: Canvas) {
        super.onDraw(canvas)
        advance()
        canvas.drawCircle(x, y, radius, ballPaint)
        canvas.drawCircle(x - radius * 0.3f, y - radius * 0.3f, radius * 0.25f, highlightPaint)
        canvas.drawText("BouncingBall · bounces: $bounceCount", 40f, 80f, textPaint)
        // postInvalidateOnAnimation drives the next frame; no manual
        // scheduling needed.
        postInvalidateOnAnimation()
    }

    private fun advance() {
        val now = System.nanoTime()
        if (lastNanos == 0L) {
            lastNanos = now
            return
        }
        val dt = (now - lastNanos) / 1_000_000_000f
        lastNanos = now
        if (dt <= 0f || dt > 0.25f) return

        x += vx * dt
        y += vy * dt

        val minX = radius
        val maxX = (width - radius)
        val minY = radius
        val maxY = (height - radius)

        if (x < minX) { x = minX; vx = kotlin.math.abs(vx); bounce("left") }
        else if (x > maxX) { x = maxX; vx = -kotlin.math.abs(vx); bounce("right") }
        if (y < minY) { y = minY; vy = kotlin.math.abs(vy); bounce("top") }
        else if (y > maxY) { y = maxY; vy = -kotlin.math.abs(vy); bounce("bottom") }
    }

    private fun bounce(wall: String) {
        bounceCount++
        Log.i(TAG, "bounce wall=$wall count=$bounceCount x=$x y=$y")
    }
}
