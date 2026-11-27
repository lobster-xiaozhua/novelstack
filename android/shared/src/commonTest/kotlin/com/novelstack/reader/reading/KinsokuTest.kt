package com.novelstack.reader.reading

import kotlin.test.Test
import kotlin.test.assertEquals

class KinsokuTest {

    private val wj = "\u2060"

    @Test
    fun insertsWordJoinerBeforeClosingPunctuation() {
        assertEquals(
            "你好${wj}，世界${wj}。",
            Kinsoku.apply("你好，世界。"),
        )
    }

    @Test
    fun leavesPlainTextUntouched() {
        assertEquals("纯文字没有标点", Kinsoku.apply("纯文字没有标点"))
    }

    @Test
    fun handlesQuotesAndBrackets() {
        assertEquals(
            "他说${wj}：“好${wj}”",
            Kinsoku.apply("他说：“好”"),
        )
    }

    @Test
    fun emptyStaysEmpty() {
        assertEquals("", Kinsoku.apply(""))
    }

    @Test
    fun newlinesAreNotModified() {
        val input = "第一行，\n第二行。"
        assertEquals("第一行${wj}，\n第二行${wj}。", Kinsoku.apply(input))
    }
}
