package com.novelstack.reader.search

import com.github.promeg.pinyinhelper.Pinyin

/**
 * 基于 TinyPinyin 的实现。
 *
 * 说明：
 *  - TinyPinyin 体积小（约 250KB），只带常用字表，不做词组分词；
 *  - 多音字取词典首读音，对书名/作者这类短文本足够；
 *  - 非汉字（英文字母、数字）原样保留并转小写，便于中英混排书名命中。
 */
object TinyPinyinProvider : PinyinProvider {

    override fun full(text: String): String {
        if (text.isEmpty()) return ""
        val sb = StringBuilder(text.length * 3)
        for (ch in text) {
            when {
                Pinyin.isChinese(ch) -> sb.append(Pinyin.toPinyin(ch).lowercase())
                ch.isLetterOrDigit() -> sb.append(ch.lowercaseChar())
                // 空格与标点直接丢弃：拼音是连续串，保留分隔符反而难匹配
                else -> Unit
            }
        }
        return sb.toString()
    }

    override fun initials(text: String): String {
        if (text.isEmpty()) return ""
        val sb = StringBuilder(text.length)
        for (ch in text) {
            when {
                Pinyin.isChinese(ch) -> {
                    val py = Pinyin.toPinyin(ch)
                    if (py.isNotEmpty()) sb.append(py[0].lowercaseChar())
                }
                ch.isLetterOrDigit() -> sb.append(ch.lowercaseChar())
                else -> Unit
            }
        }
        return sb.toString()
    }
}
