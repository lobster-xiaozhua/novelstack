package backup

import (
	"archive/tar"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// BookEntry 是备份清单中的一本书（只含元数据，绝不含正文）。
type BookEntry struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	SHA256     string `json:"sha256"`
	SizeBytes  int64  `json:"size_bytes"`
	KEKVersion int    `json:"kek_version"`
	Chapters   int    `json:"chapters"`
}

// Manifest 是备份包内的清单（明文 JSON，但只含元数据）。
type Manifest struct {
	FormatVersion    int            `json:"format_version"`
	CreatedAt        string         `json:"created_at"`
	Tool             string         `json:"tool"`
	DataLayout       string         `json:"data_layout"`
	Books            int            `json:"books"`
	Chapters         int            `json:"chapters"`
	Segments         int            `json:"segments"`
	CiphertextBytes  int64          `json:"ciphertext_bytes"`
	TTSCacheBytes    int64          `json:"tts_cache_bytes"`
	ClientIndexFiles int            `json:"client_index_files"`
	DBBytes          int64          `json:"db_bytes"`
	DBSHA256         string         `json:"db_sha256"`
	KEKVersions      map[string]int `json:"kek_versions"`
	BookList         []BookEntry    `json:"book_list"`
	OrphanFiles      int            `json:"orphan_files"`
	Notes            []string       `json:"notes,omitempty"`
}

const manifestName = "manifest.json"

// archiveFile 描述一个待打包文件。
type archiveFile struct {
	Name string // tar 内路径（始终使用 /）
	Path string // 本地绝对路径
	Size int64
}

// writeTar 把清单与文件按固定顺序写入 tar。
func writeTar(w io.Writer, manifest []byte, files []archiveFile) error {
	tw := tar.NewWriter(w)
	now := time.Unix(0, 0).UTC() // 固定时间戳，避免泄露主机 mtime
	hdr := &tar.Header{
		Name:     manifestName,
		Mode:     0o600,
		Size:     int64(len(manifest)),
		ModTime:  now,
		Typeflag: tar.TypeReg,
		Format:   tar.FormatPAX,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	if _, err := tw.Write(manifest); err != nil {
		return err
	}
	for _, f := range files {
		fh, err := os.Open(f.Path) // #nosec G304 -- 路径来自受控的数据目录遍历
		if err != nil {
			tw.Close()
			return fmt.Errorf("backup: 打开待备份文件失败: %w", err)
		}
		h := &tar.Header{
			Name:     f.Name,
			Mode:     0o600,
			Size:     f.Size,
			ModTime:  now,
			Typeflag: tar.TypeReg,
			Format:   tar.FormatPAX,
		}
		if err := tw.WriteHeader(h); err != nil {
			fh.Close()
			tw.Close()
			return err
		}
		if _, err := io.Copy(tw, fh); err != nil {
			fh.Close()
			tw.Close()
			return fmt.Errorf("backup: 写入归档失败: %w", err)
		}
		fh.Close()
	}
	return tw.Close()
}

// extractLimits 限制解包规模，避免恶意/损坏包撑爆磁盘。
type extractLimits struct {
	MaxFiles int
	MaxBytes int64
}

var defaultExtractLimits = extractLimits{MaxFiles: 2_000_000, MaxBytes: 256 << 30}

// extractTar 解包到 dest，拒绝绝对路径、.. 穿越、符号链接与设备文件。
func extractTar(r io.Reader, dest string, limits extractLimits) (files int, total int64, err error) {
	tr := tar.NewReader(r)
	seen := make(map[string]struct{}, 1024)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return files, total, nil
		}
		if err != nil {
			return files, total, fmt.Errorf("backup: 读取归档失败: %w", err)
		}
		name, err := safeArchiveName(h.Name)
		if err != nil {
			return files, total, err
		}
		if _, dup := seen[name]; dup {
			return files, total, fmt.Errorf("backup: 归档中存在重复条目 %q", name)
		}
		seen[name] = struct{}{}
		target := filepath.Join(dest, filepath.FromSlash(name))
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o700); err != nil {
				return files, total, err
			}
			continue
		case tar.TypeReg:
			// 继续处理
		default:
			return files, total, fmt.Errorf("backup: 归档包含不支持的文件类型 %q", h.Name)
		}
		if h.Size < 0 || h.Size > limits.MaxBytes {
			return files, total, fmt.Errorf("backup: 归档条目过大 %q", name)
		}
		total += h.Size
		if total > limits.MaxBytes {
			return files, total, fmt.Errorf("backup: 解包总量超过上限")
		}
		files++
		if files > limits.MaxFiles {
			return files, total, fmt.Errorf("backup: 文件数超过上限")
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return files, total, err
		}
		f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return files, total, fmt.Errorf("backup: 创建文件失败: %w", err)
		}
		if _, err := io.Copy(f, tr); err != nil {
			f.Close()
			return files, total, fmt.Errorf("backup: 解包写入失败: %w", err)
		}
		if err := f.Close(); err != nil {
			return files, total, err
		}
	}
}

// safeArchiveName 只允许 manifest.json 与 data/ 下的相对路径。
func safeArchiveName(name string) (string, error) {
	clean := path.Clean(strings.TrimPrefix(name, "./"))
	if clean == "." || strings.HasPrefix(clean, "../") || path.IsAbs(clean) {
		return "", fmt.Errorf("backup: 非法归档路径 %q", name)
	}
	if clean == manifestName || strings.HasPrefix(clean, "data/") {
		if strings.Contains(clean, "..") {
			return "", fmt.Errorf("backup: 非法归档路径 %q", name)
		}
		return clean, nil
	}
	return "", fmt.Errorf("backup: 归档包含非预期路径 %q", name)
}

// writeManifestJSON 序列化清单。
func writeManifestJSON(m *Manifest) ([]byte, error) {
	return json.MarshalIndent(m, "", "  ")
}

// ReadManifest 从已解包目录读取并校验清单。
func ReadManifest(dir string) (*Manifest, error) {
	raw, err := os.ReadFile(filepath.Join(dir, manifestName)) // #nosec G304
	if err != nil {
		return nil, fmt.Errorf("backup: 读取清单失败: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("backup: 清单不是合法 JSON: %w", err)
	}
	if m.FormatVersion != FormatVersion {
		return nil, fmt.Errorf("%w: 清单版本 %d", ErrFormat, m.FormatVersion)
	}
	if m.DBSHA256 == "" {
		return nil, fmt.Errorf("%w: 清单缺少数据库校验和", ErrFormat)
	}
	return &m, nil
}
