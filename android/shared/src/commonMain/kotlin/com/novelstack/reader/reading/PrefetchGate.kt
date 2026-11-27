package com.novelstack.reader.reading

/**
 * 章节预读触发判定（纯逻辑，可单测）。
 *
 * 规则：当前章阅读进度 >= 阈值（默认 80%）、存在下一章、且「本章尚未触发过预读」。
 * 同一章只触发一次，避免滚动时反复发起网络请求；切到下一章后自然重新武装。
 */
class PrefetchGate(private val threshold: Float = 0.8f) {

    private var armedForChapter: Int = -1

    /**
     * @param chapterIndex 当前章节序号
     * @param progress 当前章阅读进度 0..1
     * @param hasNext 是否存在下一章
     * @return 本次是否应发起「预拉下一章」
     */
    fun shouldTrigger(chapterIndex: Int, progress: Float, hasNext: Boolean): Boolean {
        if (!hasNext) return false
        if (progress < threshold) return false
        if (armedForChapter == chapterIndex) return false
        armedForChapter = chapterIndex
        return true
    }

    /** 手动重置（例如用户切换了书）。 */
    fun reset() {
        armedForChapter = -1
    }
}
