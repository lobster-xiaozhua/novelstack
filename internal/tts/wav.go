package tts

import (
	"encoding/binary"
	"fmt"
	"math"
)

// ValidateWAV 做最小结构校验：RIFF 头 + WAVE 标识 + 合理的 data 块长度。
// 不解析全部 chunk 细节（本阶段只接受引擎直出的标准 PCM WAV）。
func ValidateWAV(b []byte) error {
	if len(b) < 44 {
		return fmt.Errorf("%w: 音频过短 (%d 字节)", ErrBadAudio, len(b))
	}
	if string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return fmt.Errorf("%w: 缺少 RIFF/WAVE 头", ErrBadAudio)
	}
	// fmt chunk
	if string(b[12:16]) != "fmt " {
		return fmt.Errorf("%w: 缺少 fmt chunk", ErrBadAudio)
	}
	// 找到 data chunk（允许 fmt 后存在 LIST 等扩展块）
	idx := 12
	for idx+8 <= len(b) {
		id := string(b[idx : idx+4])
		size := int(binary.LittleEndian.Uint32(b[idx+4 : idx+8]))
		if size < 0 || idx+8+size > len(b) {
			// data 块长度可能因流式写入略有出入，容忍 data 为最后一个块
			if id == "data" {
				if idx+8 > len(b) {
					return fmt.Errorf("%w: data 块截断", ErrBadAudio)
				}
				return nil
			}
			return fmt.Errorf("%w: chunk 长度越界", ErrBadAudio)
		}
		if id == "data" {
			return nil
		}
		idx += 8 + size
		if size%2 == 1 {
			idx++ // RIFF 规范：奇数长度需 1 字节填充
		}
	}
	return fmt.Errorf("%w: 未找到 data chunk", ErrBadAudio)
}

// ToneWAV 生成确定性测试音频（mock Provider 用）：16kHz 单声道 16bit PCM，
// 时长随文本长度线性增长并做上下限截断，便于验证“首片即可播放”。
func ToneWAV(textRunes int, freqHz float64) []byte {
	const (
		sampleRate = 16000
		bits       = 16
		channels   = 1
	)
	durMS := textRunes * 40 // 每字约 40ms
	if durMS < 120 {
		durMS = 120
	}
	if durMS > 2000 {
		durMS = 2000
	}
	samples := sampleRate * durMS / 1000
	dataLen := samples * channels * bits / 8

	buf := make([]byte, 44+dataLen)
	copy(buf[0:4], "RIFF")
	binary.LittleEndian.PutUint32(buf[4:8], uint32(36+dataLen))
	copy(buf[8:12], "WAVE")
	copy(buf[12:16], "fmt ")
	binary.LittleEndian.PutUint32(buf[16:20], 16)
	binary.LittleEndian.PutUint16(buf[20:22], 1) // PCM
	binary.LittleEndian.PutUint16(buf[22:24], channels)
	binary.LittleEndian.PutUint32(buf[24:28], sampleRate)
	binary.LittleEndian.PutUint32(buf[28:32], sampleRate*channels*bits/8)
	binary.LittleEndian.PutUint16(buf[32:34], channels*bits/8)
	binary.LittleEndian.PutUint16(buf[34:36], bits)
	copy(buf[36:40], "data")
	binary.LittleEndian.PutUint32(buf[40:44], uint32(dataLen))

	if freqHz <= 0 {
		freqHz = 220
	}
	for i := 0; i < samples; i++ {
		// 首尾 10ms 淡入淡出，避免爆音
		env := 1.0
		fade := sampleRate / 100
		if i < fade {
			env = float64(i) / float64(fade)
		} else if i > samples-fade {
			env = float64(samples-i) / float64(fade)
		}
		v := 0.25 * env * math.Sin(2*math.Pi*freqHz*float64(i)/sampleRate)
		binary.LittleEndian.PutUint16(buf[44+i*2:], uint16(int16(v*32767)))
	}
	return buf
}
