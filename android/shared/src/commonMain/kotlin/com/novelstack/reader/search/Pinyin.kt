package com.novelstack.reader.search

/**
 * 拼音能力抽象。
 *
 * commonMain 不能直接引用 TinyPinyin（JVM 库），因此以接口注入：
 *  - Android 端由 [com.novelstack.reader.search.TinyPinyinProvider] 实现；
 *  - 单测 / 其他平台使用 [None]（只保留原文，拼音列留空）。
 *
 * 索引策略：书名与作者各生成「全拼」与「首字母」，与原文一起写入 books 表，
 * 搜索时用 LIKE 匹配。书籍数量很少（通常几十本），比维护第二张 FTS 表更简单可靠。
 */
interface PinyinProvider {
    /** 全拼：`小说` → `xiaoshuo`；非汉字只保留字母数字。 */
    fun full(text: String): String

    /** 首字母：`小说` → `xs`。 */
    fun initials(text: String): String

    /** 判断查询串是否只含拉丁字母与数字（决定是否值得走拼音匹配）。 */
    fun isLatinQuery(query: String): Boolean {
        if (query.isEmpty()) return false
        return query.all { it.isLetterOrDigit() && it.code < 128 }
    }

    object None : PinyinProvider {
        override fun full(text: String): String = ""
        override fun initials(text: String): String = ""
    }
}
