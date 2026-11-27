package com.novelstack.reader.reading

/**
 * 长按查词：从正文里按字符下标取一个「词」。
 * 纯逻辑，放在 commonMain 以便单测；UI 负责把点击坐标换算成字符下标。
 */
object WordPicker {
    /**
     * 返回包含 [index] 的连续词：
     *  - CJK 汉字：连续汉字，总长不超过 [maxCjk]（避免选中整段）；
     *  - 拉丁字母/数字：连续字母数字；
     *  - 其它标点/空白：返回空串（UI 可提示「未选中有效文字」）。
     */
    fun pick(text: String, index: Int, maxCjk: Int = 12): String {
        if (text.isEmpty()) return ""
        val budget = maxCjk.coerceAtLeast(1)
        val i = index.coerceIn(0, text.length - 1)
        val ch = text[i]
        if (!isWordChar(ch)) return ""
        val sameClass: (Char) -> Boolean = if (isCjk(ch)) {
            { c: Char -> isCjk(c) }
        } else {
            { c: Char -> c.isLetterOrDigit() }
        }
        var start = i
        var end = i
        // 总长度（end - start + 1）封顶，左右交替扩展：先左后右，保证单击落在词中时尽量居中。
        while (start > 0 && sameClass(text[start - 1]) && (end - start + 1) < budget) {
            start--
            if (end < text.length - 1 && sameClass(text[end + 1]) && (end - start + 1) < budget) end++
        }
        while (end < text.length - 1 && sameClass(text[end + 1]) && (end - start + 1) < budget) end++
        return text.substring(start, end + 1)
    }

    private fun isWordChar(c: Char): Boolean = isCjk(c) || c.isLetterOrDigit()

    private fun isCjk(c: Char): Boolean =
        c in '\u4E00'..'\u9FFF' || c in '\u3400'..'\u4DBF'
}
