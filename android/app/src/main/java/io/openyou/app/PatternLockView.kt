package io.openyou.app

import android.content.Context
import android.graphics.Canvas
import android.graphics.Color
import android.graphics.Paint
import android.util.AttributeSet
import android.view.MotionEvent
import android.view.View
import kotlin.math.hypot

/** Minimal 9-dot pattern lock reporting ordered dot indices; never persists anything. */
class PatternLockView @JvmOverloads constructor(
    context: Context,
    attrs: AttributeSet? = null,
    defStyle: Int = 0,
) : View(context, attrs, defStyle) {

    /** Invoked with the captured dot sequence when the finger lifts. */
    var onPatternComplete: ((List<Int>) -> Unit)? = null

    private val selected = mutableListOf<Int>()
    private var curX = 0f
    private var curY = 0f
    private var tracking = false

    private val cx = FloatArray(9)
    private val cy = FloatArray(9)
    private var dotRadius = 0f
    private var hitRadius = 0f

    private val dotPaint = Paint(Paint.ANTI_ALIAS_FLAG).apply {
        color = Color.parseColor("#8b949e"); style = Paint.Style.FILL
    }
    private val dotSelectedPaint = Paint(Paint.ANTI_ALIAS_FLAG).apply {
        color = Color.parseColor("#7C4DFF"); style = Paint.Style.FILL
    }
    private val linePaint = Paint(Paint.ANTI_ALIAS_FLAG).apply {
        color = Color.parseColor("#7C4DFF"); style = Paint.Style.STROKE
        strokeWidth = 10f; strokeCap = Paint.Cap.ROUND; strokeJoin = Paint.Join.ROUND
    }

    override fun onSizeChanged(w: Int, h: Int, oldw: Int, oldh: Int) {
        super.onSizeChanged(w, h, oldw, oldh)
        val size = minOf(w, h).toFloat()
        val marginX = (w - size) / 2f
        val marginY = (h - size) / 2f
        val step = size / 4f // three gaps of equal width around the 3 columns
        for (i in 0 until 9) {
            val row = i / 3
            val col = i % 3
            cx[i] = marginX + step * (col + 1)
            cy[i] = marginY + step * (row + 1)
        }
        dotRadius = step * 0.24f
        hitRadius = step * 0.50f
    }

    override fun onDraw(canvas: Canvas) {
        super.onDraw(canvas)
        for (i in 1 until selected.size) {
            val a = selected[i - 1]; val b = selected[i]
            canvas.drawLine(cx[a], cy[a], cx[b], cy[b], linePaint)
        }
        if (tracking && selected.isNotEmpty()) {
            val last = selected.last()
            canvas.drawLine(cx[last], cy[last], curX, curY, linePaint)
        }
        for (i in 0 until 9) {
            if (selected.contains(i)) {
                canvas.drawCircle(cx[i], cy[i], dotRadius * 1.6f, dotSelectedPaint)
            } else {
                canvas.drawCircle(cx[i], cy[i], dotRadius, dotPaint)
            }
        }
    }

    override fun onTouchEvent(event: MotionEvent): Boolean {
        when (event.actionMasked) {
            MotionEvent.ACTION_DOWN -> {
                selected.clear()
                tracking = true
                curX = event.x; curY = event.y
                hitTest(event.x, event.y)
                invalidate()
                return true
            }
            MotionEvent.ACTION_MOVE -> {
                if (!tracking) return true
                curX = event.x; curY = event.y
                // Check historical positions so a fast swipe can't skip a dot.
                for (h in 0 until event.historySize) {
                    hitTest(event.getHistoricalX(h), event.getHistoricalY(h))
                }
                hitTest(event.x, event.y)
                invalidate()
                return true
            }
            MotionEvent.ACTION_UP -> {
                if (tracking) {
                    tracking = false
                    val result = selected.toList()
                    invalidate()
                    if (result.isNotEmpty()) onPatternComplete?.invoke(result)
                }
                return true
            }
            MotionEvent.ACTION_CANCEL -> {
                reset()
                return true
            }
        }
        return super.onTouchEvent(event)
    }

    private fun hitTest(x: Float, y: Float) {
        for (i in 0 until 9) {
            if (selected.contains(i)) continue
            if (hypot((x - cx[i]).toDouble(), (y - cy[i]).toDouble()) <= hitRadius) {
                selected.add(i)
                break
            }
        }
    }

    fun reset() {
        selected.clear()
        tracking = false
        invalidate()
    }
}
