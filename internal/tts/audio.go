package tts

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"

	"novelstack/internal/books"
	"novelstack/internal/ttscache"
)

// AudioSegment 是一段可离线播放的加密音频的元信息。
// 音频密文本身通过 GET /api/tts/segments/{key} 获取（原样返回 NSAC 文件），
// 客户端用设备私钥解开 WrappedDEK，再在内存中解密音频。
type AudioSegment struct {
	Key           string `json:"key"`
	Index         int    `json:"index"`
	FileBytes     int64  `json:"file_bytes"`
	CTOffset      int    `json:"ct_offset"`
	CTLen         int    `json:"ct_len"`
	AudioNonceB64 string `json:"audio_nonce"`
	WrappedDEK    string `json:"wrapped_dek,omitempty"`
}

// AudioManifest 是 GET /api/books/{id}/chapters/{i}/audio 的响应。
type AudioManifest struct {
	BookID       string         `json:"book_id"`
	ChapterIndex int            `json:"chapter_index"`
	Count        int            `json:"count"`
	TotalBytes   int64          `json:"total_bytes"`
	Ready        bool           `json:"ready"`
	Missing      int            `json:"missing"`
	Segments     []AudioSegment `json:"segments"`
}

// PrepareAudio 合成该章缺失的音频片段并写入加密缓存（不返回明文）。
// 已有片段直接复用；返回该章片段总数，供客户端随后拉取 manifest。
func (s *Service) PrepareAudio(ctx context.Context, bookID string, chapterIndex int, ownerID, voice string, speed float64) (int, error) {
	if s.Provider == nil {
		return 0, errors.New("tts not configured")
	}
	_, plain, err := s.Books.LoadPlaintextFor(ctx, bookID, ownerID)
	if err != nil {
		return 0, err
	}
	defer zeroAudio(plain)
	chapters := SplitChapters(string(plain))
	if chapterIndex < 0 || chapterIndex >= len(chapters) {
		return 0, books.ErrNotFound
	}
	ch := chapters[chapterIndex]
	runes := []rune(string(plain))
	segs := s.buildSegments(string(runes[ch.Start:ch.End]), voice, speed)
	if len(segs) == 0 {
		return 0, errors.New("chapter has no speakable text")
	}
	maxSegs := s.MaxSegmentsPerRequest
	if maxSegs <= 0 {
		maxSegs = maxSegmentsDefault
	}
	if len(segs) > maxSegs {
		return 0, errors.New("chapter too long")
	}

	made := 0
	for _, sg := range segs {
		if err := s.Cache.Verify(sg.key); err == nil {
			s.Repo.MarkAccess(sg.key)
			continue
		} else if !errors.Is(err, ttscache.ErrNotFound) {
			return 0, fmt.Errorf("cache verify: %w", err)
		}
		audio, serr := s.synthesize(ctx, sg, voice, speed)
		if serr != nil {
			return 0, serr
		}
		if err := s.Cache.Put(sg.key, audio); err != nil {
			return 0, err
		}
		made++
		if err := s.Repo.UpsertSegment(ctx, SegmentRecord{
			CacheKey: sg.key, BookID: bookID, ChapterIndex: chapterIndex, SegmentIndex: sg.index,
			Provider: s.Provider.Name(), Voice: voice, Speed: speed, Format: FormatWAV,
			PlainBytes: int64(len(audio)), CipherBytes: s.Cache.CipherSize(sg.key),
			KEKVersion: s.Cache.CurrentVersion(),
		}); err != nil {
			return 0, err
		}
		s.Repo.MarkAccess(sg.key)
	}
	_ = s.Repo.FlushAccess(ctx)
	if s.Audit != nil {
		s.Audit.Record(ctx, ownerID, "device", "tts_audio_prepare", bookID, "ok",
			fmt.Sprintf("chapter=%d segs=%d synths=%d", chapterIndex, len(segs), made))
	}
	return len(segs), nil
}

// AudioSegments 返回该章已缓存片段的元信息（不含明文音频）。
// wrap 为 nil 时不给 wrapped_dek（例如浏览器调用），客户端会得到 ready=false 的可用段。
func (s *Service) AudioSegments(ctx context.Context, db *sql.DB, bookID string, chapterIndex int,
	ownerID string, wrap func(dek []byte) (string, error)) (*AudioManifest, error) {
	if _, err := s.Books.Store.GetOwned(ctx, bookID, ownerID, false); err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT cache_key, segment_index, cipher_bytes
		FROM tts_segments WHERE book_id = ? AND chapter_index = ? ORDER BY segment_index`,
		bookID, chapterIndex)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := &AudioManifest{BookID: bookID, ChapterIndex: chapterIndex, Segments: []AudioSegment{}}
	for rows.Next() {
		var key string
		var idx int
		var cb int64
		if err := rows.Scan(&key, &idx, &cb); err != nil {
			return nil, err
		}
		meta, merr := s.Cache.SegmentMeta(key)
		if merr != nil {
			m.Missing++
			continue
		}
		seg := AudioSegment{
			Key: key, Index: idx, FileBytes: meta.FileBytes,
			CTOffset: meta.CTOffset, CTLen: meta.CTLen,
			AudioNonceB64: base64.StdEncoding.EncodeToString(meta.AudioNonce),
		}
		if wrap != nil {
			w, werr := wrap(meta.DEK)
			if werr != nil {
				zeroAudio(meta.DEK)
				return nil, werr
			}
			seg.WrappedDEK = w
		}
		zeroAudio(meta.DEK)
		m.TotalBytes += meta.FileBytes
		m.Segments = append(m.Segments, seg)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	m.Count = len(m.Segments)
	m.Ready = m.Missing == 0 && m.Count > 0
	return m, nil
}

// SegmentOwned 校验某缓存片段是否属于该用户的书（防止按 key 枚举他人音频）。
func (s *Service) SegmentOwned(ctx context.Context, db *sql.DB, key, ownerID string) (bool, error) {
	var one int
	err := db.QueryRowContext(ctx, `SELECT 1 FROM tts_segments s
		JOIN books b ON b.id = s.book_id
		WHERE s.cache_key = ? AND b.owner_id = ? AND b.deleted_at IS NULL`, key, ownerID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// ServeSegment 返回 NSAC 加密文件原文（含头）。
// 客户端按 manifest 的 ct_offset/ct_len 取密文，用设备解包的音频 DEK 解密。
func (s *Service) ServeSegment(w http.ResponseWriter, r *http.Request, key, ownerID string) {
	if s == nil || s.Cache == nil {
		writeErr(w, http.StatusServiceUnavailable, "tts not configured")
		return
	}
	p, err := s.Cache.PathFor(key)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid segment key")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "private, max-age=0, must-revalidate")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeFile(w, r, p)
}

// zeroAudio 清零敏感内存。
func zeroAudio(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
