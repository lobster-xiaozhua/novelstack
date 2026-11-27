package tts

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"novelstack/internal/audit"
	"novelstack/internal/books"
	"novelstack/internal/cryptobox"
	"novelstack/internal/ttscache"
)

// Service 编排「章节明文 → 分片 → 片段缓存 → 合成 → 加密缓存 → 流式输出」。
// 它不持有任何模型：合成一律通过 Provider 外部调用。
type Service struct {
	Books *books.Service
	Cache *ttscache.Store
	Repo  *Repo
	// Provider 为 nil 表示未启用 TTS（TTS_PROVIDER=off）
	Provider Provider
	// ProviderKind 是配置值（off/mock/piper/openai），用于指标展示
	ProviderKind    string
	Audit           *audit.Logger
	MaxSegmentRunes int
	DefaultVoice    string
	DefaultSpeed    float64
	SegmentTimeout  time.Duration
	// MaxSegmentsPerRequest 限制单次朗读的片段数，避免一次请求打满 CPU
	MaxSegmentsPerRequest int
	// ProtectWindow 是缓存淘汰的保护窗口：窗口内访问过的片段不删（默认 5 分钟）。
	ProtectWindow time.Duration

	// ---- 第九阶段：旁白 / 对话双音色 ----
	// DialogueVoice 为对话音色；为空表示不启用（全部片段用请求里的 voice）。
	DialogueVoice string
	// DialoguePattern 识别对话段落；必须含捕获组（组 1 为对话正文）。
	DialoguePattern string
	dialogueRe      *regexp.Regexp
	dialogueOnce    sync.Once

	refMu sync.Mutex
	// refs 记录本进程正在流式输出的片段引用计数，清理时跳过。
	refs map[string]int
}

const maxSegmentsDefault = 1000

var voicePattern = regexp.MustCompile(`^[A-Za-z0-9_.\-]{1,64}$`)

// seg 是一个待朗读片段及其缓存键。
type seg struct {
	index int
	text  string
	key   string
	// voice 是该片段使用的音色：旁白与对话可能不同。
	voice string
}

// Speak 处理 GET /api/books/{id}/chapters/{idx}/speak。
//
// 流程：解密整本 → 切分章节与片段 → 计算片段指纹 → 全量预检缓存完整性 →
// 逐段（命中则解密，未命中则合成并加密落盘）写响应并 flush。
// 首片写完后即可播放，不需要等整章合成完成。
func (s *Service) Speak(w http.ResponseWriter, r *http.Request, bookID string, chapterIndex int, username, ip string) {
	ctx := r.Context()
	if s.Provider == nil {
		writeErr(w, http.StatusServiceUnavailable, "tts not configured")
		return
	}
	voice, dialogueVoice, speed, ok := s.readVoices(w, r)
	if !ok {
		return
	}

	_, plain, err := s.Books.LoadPlaintextFor(ctx, bookID, username)
	if err != nil {
		s.auditBookErr(ctx, "tts_speak", bookID, username, ip, err)
		st, msg := bookErrStatus(err)
		writeErr(w, st, msg)
		return
	}
	chapters := SplitChapters(string(plain))
	if chapterIndex < 0 || chapterIndex >= len(chapters) {
		writeErr(w, http.StatusNotFound, "chapter not found")
		return
	}
	ch := chapters[chapterIndex]
	runes := []rune(string(plain))
	// 朗读同样使用净化后的文本：广告句不该被念出来。
	chapterText := s.Books.PurgeText(string(runes[ch.Start:ch.End]))

	segs := s.buildSegments(chapterText, voice, dialogueVoice, speed)
	if len(segs) == 0 {
		writeErr(w, http.StatusUnprocessableEntity, "chapter has no speakable text")
		return
	}
	maxSegs := s.MaxSegmentsPerRequest
	if maxSegs <= 0 {
		maxSegs = maxSegmentsDefault
	}
	if len(segs) > maxSegs {
		writeErr(w, http.StatusUnprocessableEntity, "chapter too long for one request")
		return
	}

	// 预检：已缓存片段必须可解密。任何篡改都在写出第一个字节之前失败，
	// 客户端不会收到半截或损坏的音频。
	for _, sg := range segs {
		if err := s.Cache.Verify(sg.key); err != nil {
			if errors.Is(err, ttscache.ErrTampered) {
				s.Audit.Record(ctx, username, ip, "tts_cache_tamper", bookID, "denied",
					fmt.Sprintf("chapter=%d seg=%d key=%s", chapterIndex, sg.index, sg.key[:12]))
				writeErr(w, http.StatusConflict, "audio cache integrity check failed")
				return
			}
			if errors.Is(err, ttscache.ErrNotFound) {
				continue
			}
			writeErr(w, http.StatusInternalServerError, "internal error")
			return
		}
	}

	mode := strings.ToLower(r.URL.Query().Get("mode"))
	sw := newSegmentWriter(w, mode, ch.Index, len(segs))
	if sw == nil {
		writeErr(w, http.StatusBadRequest, "unsupported mode")
		return
	}

	// 整章所有片段在请求期间保持引用：缓存清理/淘汰会跳过这些键，
	// 避免朗读到一半片段被删导致中途 404。
	release := s.acquireKeys(segs)
	defer release()

	var (
		hits, synths int
		written      int64
		failedAt     = -1
		failClass    string
	)
	started := time.Now()
	// 无论成功、部分失败还是客户端断开，都刷新访问时间并记一条朗读统计（无文本）。
	defer func() {
		ctx2, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = s.Repo.FlushAccess(ctx2)
		_ = s.Repo.LogSpeak(ctx2, bookID, chapterIndex, len(segs), hits, synths, written, time.Since(started))
	}()
	if err := sw.begin(); err != nil {
		return
	}

	for i, sg := range segs {
		// 客户端断开则立即停止，避免继续消耗 TTS 资源
		if ctx.Err() != nil {
			return
		}
		if audio, err := s.Cache.Get(sg.key); err == nil {
			if err := sw.writeSegment(sg.index, audio); err != nil {
				return
			}
			hits++
			written += int64(len(audio))
			_ = s.Repo.TouchHit(ctx, sg.key)
			s.Repo.MarkAccess(sg.key)
			if i%8 == 7 {
				_ = s.Repo.FlushAccess(ctx)
			}
			continue
		} else if !errors.Is(err, ttscache.ErrNotFound) {
			// 预检通过后又被改动（TOCTOU）：绝不输出可疑音频
			failedAt, failClass = sg.index, "cache_read_error"
			sw.failSegment(sg.index, "audio_unavailable")
			break
		}

		audio, err := s.synthesize(ctx, sg, speed)
		if err != nil {
			failedAt, failClass = sg.index, classifyErr(err)
			sw.failSegment(sg.index, "synthesis_failed")
			break
		}
		synths++
		if err := s.Cache.Put(sg.key, audio); err != nil {
			// 缓存写失败不阻断播放，但要留痕
			s.Audit.Record(ctx, username, ip, "tts_cache_write_error", bookID, "error",
				fmt.Sprintf("chapter=%d seg=%d", chapterIndex, sg.index))
		} else {
			_ = s.Repo.UpsertSegment(ctx, SegmentRecord{
				CacheKey:     sg.key,
				BookID:       bookID,
				ChapterIndex: chapterIndex,
				SegmentIndex: sg.index,
				Provider:     s.Provider.Name(),
				Voice:        sg.voice,
				Speed:        speed,
				Format:       FormatWAV,
				PlainBytes:   int64(len(audio)),
				CipherBytes:  s.Cache.CipherSize(sg.key),
				KEKVersion:   s.Cache.CurrentVersion(),
			})
			s.Repo.MarkAccess(sg.key)
			if i%8 == 7 {
				_ = s.Repo.FlushAccess(ctx)
			}
		}
		if err := sw.writeSegment(sg.index, audio); err != nil {
			return
		}
		written += int64(len(audio))
	}

	detail := fmt.Sprintf("chapter=%d segs=%d hits=%d synths=%d bytes=%d",
		chapterIndex, len(segs), hits, synths, written)
	if failedAt >= 0 {
		s.Audit.Record(ctx, username, ip, "tts_speak_partial", bookID, "error",
			fmt.Sprintf("%s failed_seg=%d class=%s", detail, failedAt, failClass))
		return
	}
	sw.finish()
	s.Audit.Record(ctx, username, ip, "tts_speak", bookID, "ok", detail)
}

// buildSegments 切分章节并计算片段指纹（跳过纯空白片段）。
//
// 双音色：先按 DialoguePattern 把文本切成「旁白/对话」交替的段落，
// 再对每段做等长切分。对话段用 dialogueVoice，其余用 voice。
// 缓存键包含实际音色，因此同一句话在两种音色下不会互相覆盖。
func (s *Service) buildSegments(chapterText, voice, dialogueVoice string, speed float64) []seg {
	spans := SplitDialogue(chapterText, s.dialogueRegexp())
	out := make([]seg, 0, len(spans))
	index := 0
	for _, sp := range spans {
		if isSilentSpan(sp.Text) {
			continue
		}
		segVoice := voice
		if sp.Dialogue && dialogueVoice != "" {
			segVoice = dialogueVoice
		}
		for _, p := range Segment(sp.Text, s.MaxSegmentRunes) {
			if IsSilent(p) {
				index++
				continue
			}
			out = append(out, seg{
				index: index,
				text:  p,
				voice: segVoice,
				key:   CacheKey(s.Provider.Name(), segVoice, speed, FormatWAV, p),
			})
			index++
		}
	}
	return out
}

// dialogueRegexp 惰性编译对话识别正则（配置错误时不阻断朗读，只降级为单音色）。
func (s *Service) dialogueRegexp() *regexp.Regexp {
	if s.DialogueVoice == "" {
		return nil
	}
	s.dialogueOnce.Do(func() {
		pattern := s.DialoguePattern
		if pattern == "" {
			pattern = `[“"「『]([^”"」』\n]{1,400})[”"」』]`
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			// 配置错误不阻断朗读：降级为单音色，并留下一次性日志。
			log.Printf("tts: TTS_DIALOGUE_PATTERN 无效，已降级为单音色: %v", err)
			return
		}
		s.dialogueRe = re
	})
	return s.dialogueRe
}

// synthesize 单片段合成：独立超时、记录耗时与错误类别（不记录文本）。
func (s *Service) synthesize(ctx context.Context, sg seg, speed float64) ([]byte, error) {
	timeout := s.SegmentTimeout
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	sctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start := time.Now()
	audio, err := s.Provider.Synthesize(sctx, NormalizeText(sg.text), sg.voice, speed)
	dur := time.Since(start)
	class := classifyErr(err)
	result := "ok"
	if err != nil {
		result = "error"
		if class == "timeout" {
			result = "timeout"
		}
	}
	// 只记录片段键与耗时，绝不含文本或音频
	_ = s.Repo.LogSynth(ctx, sg.key, s.Provider.Name(), result, dur, class)
	if err != nil {
		return nil, err
	}
	if err := ValidateWAV(audio); err != nil {
		_ = s.Repo.LogSynth(ctx, sg.key, s.Provider.Name(), "error", dur, "bad_audio")
		return nil, err
	}
	return audio, nil
}

// readVoices 解析并校验 voice / dialogue_voice / speed；不合法时已写出 400。
//
// narration 来自 ?voice=（缺省用 TTS_VOICE）；
// dialogue 来自 ?dialogue_voice=（缺省用 TTS_DIALOGUE_VOICE），为空表示单音色。
func (s *Service) readVoices(w http.ResponseWriter, r *http.Request) (narration, dialogue string, speed float64, ok bool) {
	narration = r.URL.Query().Get("voice")
	if narration == "" {
		narration = s.DefaultVoice
	}
	if narration != "" && !voicePattern.MatchString(narration) {
		writeErr(w, http.StatusBadRequest, "invalid voice")
		return "", "", 0, false
	}
	dialogue = r.URL.Query().Get("dialogue_voice")
	if dialogue == "" {
		dialogue = s.DialogueVoice
	}
	if dialogue != "" && !voicePattern.MatchString(dialogue) {
		writeErr(w, http.StatusBadRequest, "invalid dialogue_voice")
		return "", "", 0, false
	}
	speed = s.DefaultSpeed
	if speed <= 0 {
		speed = 1.0
	}
	if v := r.URL.Query().Get("speed"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || f < 0.5 || f > 2.0 {
			writeErr(w, http.StatusBadRequest, "invalid speed (0.5-2.0)")
			return "", "", 0, false
		}
		speed = f
	}
	return narration, dialogue, speed, true
}

func (s *Service) auditBookErr(ctx context.Context, action, bookID, username, ip string, err error) {
	switch {
	case errors.Is(err, books.ErrNotFound):
		s.Audit.Record(ctx, username, ip, action, bookID, "denied", "not found")
	case errors.Is(err, cryptobox.ErrTampered):
		s.Audit.Record(ctx, username, ip, action+"_tamper", bookID, "denied", "integrity check failed")
	default:
		s.Audit.Record(ctx, username, ip, action, bookID, "error", "read failed")
	}
}

// bookErrStatus 复用书籍域的错误映射（对外模糊）。
func bookErrStatus(err error) (int, string) {
	switch {
	case errors.Is(err, books.ErrNotFound):
		return http.StatusNotFound, "not found"
	case errors.Is(err, cryptobox.ErrTampered):
		return http.StatusConflict, "content integrity check failed"
	case errors.Is(err, cryptobox.ErrBadFormat):
		return http.StatusUnprocessableEntity, "bad stored format"
	default:
		return http.StatusInternalServerError, "internal error"
	}
}

// ---- 响应写入：chunked 二进制（默认） 与 SSE 变体 ----

// segmentWriter 抽象两种输出形态，便于同一编排逻辑复用。
type segmentWriter interface {
	begin() error
	writeSegment(index int, audio []byte) error
	failSegment(index int, code string)
	finish()
}

func newSegmentWriter(w http.ResponseWriter, mode string, chapter, total int) segmentWriter {
	switch mode {
	case "", "stream", "chunked":
		return &wavWriter{w: w, chapter: chapter, total: total}
	case "sse":
		return &sseWriter{w: w, chapter: chapter, total: total}
	default:
		return nil
	}
}

// wavWriter 顺序推流多个 WAV 片段（客户端按 RIFF 头切分后拼接播放）。
type wavWriter struct {
	w       http.ResponseWriter
	chapter int
	total   int
	started bool
}

func (s *wavWriter) begin() error {
	h := s.w.Header()
	h.Set("Content-Type", "audio/wav")
	h.Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", fmt.Sprintf("chapter-%d.wav", s.chapter)))
	// 明确告知这是“WAV 片段序列”，客户端需按 RIFF 头切分
	h.Set("X-TTS-Stream", "wav-segments")
	h.Set("X-TTS-Segments", strconv.Itoa(s.total))
	// 禁止中间层缓冲：Caddy 侧另有 flush_interval -1
	h.Set("X-Accel-Buffering", "no")
	h.Set("Cache-Control", "no-store")
	s.w.WriteHeader(http.StatusOK)
	s.started = true
	flush(s.w)
	return nil
}

func (s *wavWriter) writeSegment(index int, audio []byte) error {
	if _, err := s.w.Write(audio); err != nil {
		return err
	}
	flush(s.w)
	return nil
}

func (s *wavWriter) failSegment(index int, code string) {
	// 二进制流已开始：无法改状态码，直接结束响应让客户端感知截断。
	// 具体错误已在审计中留痕，不向客户端泄漏引擎细节。
}

func (s *wavWriter) finish() { flush(s.w) }

// sseWriter 是可选变体：带事件元数据（segment / segment_error），
// 方便浏览器端直接按 base64 解码播放。
type sseWriter struct {
	w       http.ResponseWriter
	chapter int
	total   int
}

func (s *sseWriter) begin() error {
	h := s.w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	h.Set("Connection", "keep-alive")
	s.w.WriteHeader(http.StatusOK)
	fmt.Fprintf(s.w, "event: start\ndata: {\"chapter\":%d,\"segments\":%d,\"format\":\"%s\"}\n\n", s.chapter, s.total, FormatWAV)
	flush(s.w)
	return nil
}

func (s *sseWriter) writeSegment(index int, audio []byte) error {
	payload, err := json.Marshal(map[string]any{
		"index":  index,
		"format": FormatWAV,
		"audio":  base64.StdEncoding.EncodeToString(audio),
	})
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(s.w, "event: segment\ndata: %s\n\n", payload); err != nil {
		return err
	}
	flush(s.w)
	return nil
}

func (s *sseWriter) failSegment(index int, code string) {
	payload, _ := json.Marshal(map[string]any{"index": index, "error": code})
	fmt.Fprintf(s.w, "event: segment_error\ndata: %s\n\n", payload)
	flush(s.w)
}

func (s *sseWriter) finish() {
	fmt.Fprint(s.w, "event: done\ndata: {}\n\n")
	flush(s.w)
}

// flush 立即把已写数据推给客户端（禁用代理缓冲的关键）。
func flush(w http.ResponseWriter) {
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// ---- 指标 ----

// Stats 处理 GET /api/admin/tts/stats：只读指标，不含任何章节文本。
func (s *Service) Stats(w http.ResponseWriter, r *http.Request, username, ip string) {
	ctx := r.Context()
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 500 {
			limit = n
		}
	}
	segments, bytes, err := s.Cache.Stats()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	total, okCount, avgMs, err := s.Repo.SynthSummary(ctx, limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	synthP50, synthP95, err := s.Repo.SynthLatency(ctx, limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	speakSegs, speakHits, speakSynths, speakP50, speakP95, err := s.Repo.SpeakTotals(ctx, 200)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	providerName := ""
	health, healthErr := "disabled", ""
	if s.Provider != nil {
		providerName = s.Provider.Name()
		hctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		if err := s.Provider.Health(hctx); err != nil {
			health, healthErr = "unavailable", "probe failed"
		} else {
			health = "ok"
		}
	}
	rate := 0.0
	if total > 0 {
		rate = float64(okCount) / float64(total)
	}
	hitRate := 0.0
	if speakSegs > 0 {
		hitRate = float64(speakHits) / float64(speakSegs)
	}
	s.Audit.Record(ctx, username, ip, "tts_stats_view", "", "ok", fmt.Sprintf("limit=%d", limit))
	writeJSON(w, http.StatusOK, map[string]any{
		"generated_at": time.Now().UTC().Format(time.RFC3339),
		"provider": map[string]any{
			"kind":   s.ProviderKind,
			"name":   providerName,
			"health": health,
			"error":  healthErr,
		},
		"cache": map[string]any{
			"dir":          "tts-cache",
			"segments":     segments,
			"cipher_bytes": bytes,
			"hit_rate":     hitRate,
		},
		"synth": map[string]any{
			"window":       limit,
			"total":        total,
			"ok":           okCount,
			"failed":       total - okCount,
			"success_rate": rate,
			"avg_ms":       avgMs,
			"p50_ms":       synthP50,
			"p95_ms":       synthP95,
		},
		"speak": map[string]any{
			"window":   200,
			"segments": speakSegs,
			"hits":     speakHits,
			"synths":   speakSynths,
			"hit_rate": hitRate,
			"p50_ms":   speakP50,
			"p95_ms":   speakP95,
		},
		"config": map[string]any{
			"max_segment_chars": s.MaxSegmentRunes,
			"default_voice":     s.DefaultVoice,
			"default_speed":     s.DefaultSpeed,
			"format":            FormatWAV,
			"protect_window_s":  int(s.protectWindow().Seconds()),
		},
	})
}

// ---- 数据访问 ----

// Repo 封装 tts_segments / tts_synth_logs / tts_speak_stats 表。
type Repo struct {
	db *sql.DB

	mu      sync.Mutex
	pending map[string]struct{}
}

// NewRepo 构造 Repo。
func NewRepo(db *sql.DB) *Repo {
	return &Repo{db: db, pending: make(map[string]struct{}, 64)}
}

// SegmentRecord 是一条片段缓存元数据（不含音频与文本）。
type SegmentRecord struct {
	CacheKey     string
	BookID       string
	ChapterIndex int
	SegmentIndex int
	Provider     string
	Voice        string
	Speed        float64
	Format       string
	PlainBytes   int64
	CipherBytes  int64
	KEKVersion   int
}

// UpsertSegment 记录片段缓存元数据，并把 last_access 置为当前时间。
func (r *Repo) UpsertSegment(ctx context.Context, rec SegmentRecord) error {
	ver := rec.KEKVersion
	if ver <= 0 {
		ver = 1
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO tts_segments
		(cache_key, book_id, chapter_index, segment_index, provider, voice, speed, format,
		 plain_bytes, cipher_bytes, last_hit_at, last_access, kek_version)
		VALUES (?,?,?,?,?,?,?,?,?,?, NULL,
		        strftime('%Y-%m-%dT%H:%M:%fZ','now'), ?)
		ON CONFLICT(cache_key) DO UPDATE SET
			plain_bytes=excluded.plain_bytes,
			cipher_bytes=excluded.cipher_bytes,
			provider=excluded.provider,
			voice=excluded.voice,
			speed=excluded.speed,
			format=excluded.format,
			kek_version=excluded.kek_version`,
		rec.CacheKey, rec.BookID, rec.ChapterIndex, rec.SegmentIndex,
		rec.Provider, rec.Voice, rec.Speed, rec.Format, rec.PlainBytes, rec.CipherBytes, ver)
	return err
}

// TouchHit 更新命中时间（失败不影响主流程）。
func (r *Repo) TouchHit(ctx context.Context, key string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE tts_segments SET last_hit_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		 WHERE cache_key = ?`, key)
	return err
}

// MarkAccess 把片段加入待刷新的访问队列（降低写放大）。
func (r *Repo) MarkAccess(key string) {
	if key == "" {
		return
	}
	r.mu.Lock()
	r.pending[key] = struct{}{}
	r.mu.Unlock()
}

// FlushAccess 一次 UPDATE 批量刷新访问时间；队列为空时不写库。
func (r *Repo) FlushAccess(ctx context.Context) error {
	r.mu.Lock()
	if len(r.pending) == 0 {
		r.mu.Unlock()
		return nil
	}
	keys := make([]string, 0, len(r.pending))
	for k := range r.pending {
		keys = append(keys, k)
	}
	r.pending = make(map[string]struct{}, 64)
	r.mu.Unlock()

	// 分块以避免 SQLite 参数上限（默认 999，这里取 400 保守值）。
	for start := 0; start < len(keys); start += 400 {
		end := start + 400
		if end > len(keys) {
			end = len(keys)
		}
		chunk := keys[start:end]
		q := `UPDATE tts_segments SET last_access = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		      WHERE cache_key IN (` + placeholders(len(chunk)) + `)`
		if _, err := r.db.ExecContext(ctx, q, toAnyStrings(chunk)...); err != nil {
			// 失败放回队列，下次再试
			r.mu.Lock()
			for _, k := range chunk {
				r.pending[k] = struct{}{}
			}
			r.mu.Unlock()
			return err
		}
	}
	return nil
}

// LogSynth 记录一次合成尝试：只有片段键、耗时、结果类别，没有文本。
func (r *Repo) LogSynth(ctx context.Context, key, provider, result string, dur time.Duration, errClass string) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO tts_synth_logs(cache_key, provider, result, duration_ms, err_class) VALUES (?,?,?,?,?)`,
		key, provider, result, dur.Milliseconds(), errClass)
	return err
}

// LogSpeak 记录一次朗读请求的计数与耗时（不含文本、音频）。
func (r *Repo) LogSpeak(ctx context.Context, bookID string, chapter, segments, hits, synths int, written int64, dur time.Duration) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO tts_speak_stats
		(book_id, chapter_index, segments, hits, synths, bytes, duration_ms)
		VALUES (?,?,?,?,?,?,?)`, bookID, chapter, segments, hits, synths, written, dur.Milliseconds())
	return err
}

// SynthSummary 统计最近 limit 次合成的成功数与平均耗时。
func (r *Repo) SynthSummary(ctx context.Context, limit int) (total, ok int, avgMs float64, err error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	row := r.db.QueryRowContext(ctx, `SELECT COUNT(*),
		COALESCE(SUM(CASE WHEN result='ok' THEN 1 ELSE 0 END),0),
		COALESCE(AVG(CASE WHEN result='ok' THEN duration_ms END),0)
		FROM (SELECT result, duration_ms FROM tts_synth_logs ORDER BY id DESC LIMIT ?)`, limit)
	if err = row.Scan(&total, &ok, &avgMs); err != nil {
		return 0, 0, 0, err
	}
	return total, ok, avgMs, nil
}

// SynthLatency 返回最近 limit 次成功合成的 P50/P95 耗时（毫秒）。
func (r *Repo) SynthLatency(ctx context.Context, limit int) (p50, p95 float64, err error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := r.db.QueryContext(ctx, `SELECT duration_ms FROM (
		SELECT duration_ms FROM tts_synth_logs WHERE result='ok' ORDER BY id DESC LIMIT ?)`, limit)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()
	var ds []float64
	for rows.Next() {
		var d float64
		if err := rows.Scan(&d); err != nil {
			return 0, 0, err
		}
		ds = append(ds, d)
	}
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}
	return percentile(ds, 0.50), percentile(ds, 0.95), nil
}

// SpeakTotals 汇总最近 limit 次朗读的命中率与耗时。
func (r *Repo) SpeakTotals(ctx context.Context, limit int) (segments, hits, synths int64, p50, p95 float64, err error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	err = r.db.QueryRowContext(ctx, `SELECT
		COALESCE(SUM(segments),0), COALESCE(SUM(hits),0), COALESCE(SUM(synths),0)
		FROM (SELECT segments, hits, synths FROM tts_speak_stats ORDER BY id DESC LIMIT ?)`, limit).
		Scan(&segments, &hits, &synths)
	if err != nil {
		return 0, 0, 0, 0, 0, err
	}
	rows, err := r.db.QueryContext(ctx, `SELECT duration_ms FROM (
		SELECT duration_ms FROM tts_speak_stats ORDER BY id DESC LIMIT ?)`, limit)
	if err != nil {
		return 0, 0, 0, 0, 0, err
	}
	defer rows.Close()
	var ds []float64
	for rows.Next() {
		var d float64
		if err := rows.Scan(&d); err != nil {
			return 0, 0, 0, 0, 0, err
		}
		ds = append(ds, d)
	}
	return segments, hits, synths, percentile(ds, 0.50), percentile(ds, 0.95), rows.Err()
}

// CacheBookStat 是按书聚合的缓存占用。
type CacheBookStat struct {
	BookID   string `json:"book_id"`
	Title    string `json:"title"`
	Segments int    `json:"segments"`
	Bytes    int64  `json:"bytes"`
}

// CacheByBook 按书聚合缓存片段数与密文字节数。
func (r *Repo) CacheByBook(ctx context.Context, limit int) ([]CacheBookStat, error) {
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	rows, err := r.db.QueryContext(ctx, `SELECT s.book_id, COALESCE(b.title, ''), COUNT(*), COALESCE(SUM(s.cipher_bytes),0)
		FROM tts_segments s LEFT JOIN books b ON b.id = s.book_id
		GROUP BY s.book_id ORDER BY SUM(s.cipher_bytes) DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CacheBookStat{}
	for rows.Next() {
		var c CacheBookStat
		if err := rows.Scan(&c.BookID, &c.Title, &c.Segments, &c.Bytes); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// KeysForBook 返回某本书的全部缓存键；bookID 为空时返回全部。
func (r *Repo) KeysForBook(ctx context.Context, bookID string) ([]string, error) {
	var rows *sql.Rows
	var err error
	if bookID == "" {
		rows, err = r.db.QueryContext(ctx, `SELECT cache_key FROM tts_segments`)
	} else {
		rows, err = r.db.QueryContext(ctx, `SELECT cache_key FROM tts_segments WHERE book_id = ?`, bookID)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// DeleteSegments 删除片段索引行（分块）。
func (r *Repo) DeleteSegments(ctx context.Context, keys []string) error {
	for start := 0; start < len(keys); start += 400 {
		end := start + 400
		if end > len(keys) {
			end = len(keys)
		}
		chunk := keys[start:end]
		q := `DELETE FROM tts_segments WHERE cache_key IN (` + placeholders(len(chunk)) + `)`
		if _, err := r.db.ExecContext(ctx, q, toAnyStrings(chunk)...); err != nil {
			return err
		}
	}
	return nil
}

// SetKEKVersion 更新片段索引中的 KEK 版本（轮换后校正）。
func (r *Repo) SetKEKVersion(ctx context.Context, key string, version int) error {
	_, err := r.db.ExecContext(ctx, `UPDATE tts_segments SET kek_version=? WHERE cache_key=?`, version, key)
	return err
}

// TagAllKEKVersion 把所有片段的 KEK 版本置为指定值（轮换完成后校正）。
func (r *Repo) TagAllKEKVersion(ctx context.Context, version int) error {
	_, err := r.db.ExecContext(ctx, `UPDATE tts_segments SET kek_version=? WHERE kek_version <> ?`, version, version)
	return err
}

// PruneCandidate 是淘汰候选的一行（只含键与时间）。
type PruneCandidate struct {
	Key        string
	BookID     string
	CipherSize int64
	LastAccess string
}

// PruneCandidates 按最久未访问排序返回候选（全表最多 5000 条）。
func (r *Repo) PruneCandidates(ctx context.Context) ([]PruneCandidate, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT cache_key, book_id, cipher_bytes,
		COALESCE(last_access, last_hit_at, created_at)
		FROM tts_segments
		ORDER BY COALESCE(last_access, last_hit_at, created_at) ASC
		LIMIT 5000`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PruneCandidate
	for rows.Next() {
		var c PruneCandidate
		if err := rows.Scan(&c.Key, &c.BookID, &c.CipherSize, &c.LastAccess); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func placeholders(n int) string {
	if n <= 0 {
		return "NULL"
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func toAnyStrings(in []string) []any {
	out := make([]any, len(in))
	for i := range in {
		out[i] = in[i]
	}
	return out
}

func percentile(ds []float64, p float64) float64 {
	if len(ds) == 0 {
		return 0
	}
	sort.Float64s(ds)
	idx := int(float64(len(ds)-1) * p)
	return ds[idx]
}

// writeJSON / writeErr 与书籍 handler 保持一致的响应格式。
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
