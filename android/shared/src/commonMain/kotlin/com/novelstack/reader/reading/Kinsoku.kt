package com.novelstack.reader.reading

/**
 * 中文避头尾（kinsoku）近似实现。
 *
 * Compose/Android 的排版引擎没有暴露完整的避头尾开关，这里把「禁止出现在行首」的
 * 标点前面插入 WORD JOINER（U+2060）——它是一个零宽、不可断行的连接符，
 * 会让引擎尽量不从该标点前断行。属于业界常用的轻量近似方案。
 *
 * 注意：只影响「显示」，不改动入库的原始正文（本地 FTS 索引仍用原文）。
 */
object Kinsoku {

    private const val WORD_JOINER = "\u2060"

    // 不能出现在行首的字符：中文标点为主，附带常见英文右括号/引号。
    private const val NO_LINE_START = "，。、；：！？）】》」』”’,.;:!?)]}\"'"

    /** 在禁止行首的标点前插入 word joiner。 */
    fun apply(text: String): String {
        if (text.isEmpty()) return text
        val sb = StringBuilder(text.length + text.length / 16)
        for (ch in text) {
            if (NO_LINE_START.indexOf(ch) >= 0) {
                sb.append(WORD_JOINER)
            }
            sb.append(ch)
        }
        return sb.toString()
    }
}
