// Package assets stores immutable, content-addressed creative files.
package assets

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/songfei1983/open-ads/dsp/internal/dsp/domain"
	_ "golang.org/x/image/webp"
)

const (
	MaxContentBytes = 10 << 20
	MaxDimension    = 4096
)

var (
	ErrTooLarge           = errors.New("asset_too_large")
	ErrUnsupportedMIME    = errors.New("unsupported_mime_type")
	ErrDimensionsExceeded = errors.New("asset_dimensions_exceeded")
	ErrHashMismatch       = errors.New("asset_hash_mismatch")
	ErrInvalidImage       = errors.New("invalid_image")
	ErrInvalidAssetRef    = errors.New("invalid_asset_ref")
	ErrAssetTampered      = errors.New("asset_tampered_detected")
	ErrAssetNotFound      = errors.New("asset_not_found")
)

// Metadata contains values derived from the uploaded bytes, never request claims.
type Metadata struct {
	AssetRef  string `json:"asset_ref"`
	SHA256    string `json:"actual_sha256"`
	SizeBytes int64  `json:"actual_size_bytes"`
	Width     int    `json:"actual_width_px"`
	Height    int    `json:"actual_height_px"`
	MIMEType  string `json:"mime_type"`
}

// FileStore confines all file operations beneath a single configured root.
type FileStore struct {
	root        *os.Root
	decodeSlots chan struct{}
}

// NewFileStore opens a root for immutable asset objects.
func NewFileStore(rootPath string) (*FileStore, error) {
	if strings.TrimSpace(rootPath) == "" {
		return nil, errors.New("asset store root is required")
	}
	absoluteRoot, err := filepath.Abs(rootPath)
	if err != nil {
		return nil, fmt.Errorf("resolve asset store root: %w", err)
	}
	if err := os.MkdirAll(absoluteRoot, 0o750); err != nil {
		return nil, fmt.Errorf("create asset store root: %w", err)
	}
	canonicalRoot, err := filepath.EvalSymlinks(absoluteRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve asset store root symlinks: %w", err)
	}
	root, err := os.OpenRoot(canonicalRoot)
	if err != nil {
		return nil, fmt.Errorf("open asset store root: %w", err)
	}
	if err := root.MkdirAll(".tmp", 0o700); err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("create asset temp directory: %w", err)
	}
	tempInfo, err := root.Lstat(".tmp")
	if err != nil || !tempInfo.IsDir() || tempInfo.Mode()&os.ModeSymlink != 0 {
		_ = root.Close()
		return nil, errors.New("asset temp path must be a real directory")
	}
	return &FileStore{root: root, decodeSlots: make(chan struct{}, 2)}, nil
}

// Close releases the root directory handle.
func (s *FileStore) Close() error {
	if s == nil || s.root == nil {
		return nil
	}
	return s.root.Close()
}

// Put streams, validates, and atomically stores an immutable image.
func (s *FileStore) Put(ctx context.Context, tenantID string, source io.Reader, expectedSHA256 string) (Metadata, error) {
	if s == nil || s.root == nil || source == nil {
		return Metadata{}, errors.New("asset store and source are required")
	}
	if err := domain.ValidateUUID32(tenantID); err != nil || strings.ToLower(tenantID) != tenantID {
		return Metadata{}, ErrInvalidAssetRef
	}
	if expectedSHA256 != "" {
		if err := validateSHA256(expectedSHA256); err != nil {
			return Metadata{}, err
		}
	}
	select {
	case s.decodeSlots <- struct{}{}:
		defer func() { <-s.decodeSlots }()
	case <-ctx.Done():
		return Metadata{}, ctx.Err()
	}

	tempName, err := domain.NewUUIDv7()
	if err != nil {
		return Metadata{}, fmt.Errorf("generate asset temp name: %w", err)
	}
	tempPath := filepath.ToSlash(filepath.Join(".tmp", tempName))
	temp, err := s.root.OpenFile(tempPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return Metadata{}, fmt.Errorf("create asset temp file: %w", err)
	}
	keepTemp := true
	defer func() {
		if keepTemp {
			_ = s.root.Remove(tempPath)
		}
	}()

	contentHash := sha256.New()
	limited := &io.LimitedReader{R: source, N: MaxContentBytes + 1}
	sizeBytes, copyErr := io.Copy(io.MultiWriter(temp, contentHash), limited)
	if copyErr != nil {
		_ = temp.Close()
		return Metadata{}, fmt.Errorf("write asset temp file: %w", copyErr)
	}
	if sizeBytes > MaxContentBytes {
		_ = temp.Close()
		return Metadata{}, ErrTooLarge
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return Metadata{}, fmt.Errorf("sync asset temp file: %w", err)
	}
	if err := temp.Close(); err != nil {
		return Metadata{}, fmt.Errorf("close asset temp file: %w", err)
	}
	actualSHA256 := hex.EncodeToString(contentHash.Sum(nil))
	if expectedSHA256 != "" && actualSHA256 != expectedSHA256 {
		return Metadata{}, ErrHashMismatch
	}

	metadata, err := s.inspect(ctx, tempPath, actualSHA256, sizeBytes, tenantID)
	if err != nil {
		return Metadata{}, err
	}
	if err := s.root.Chmod(tempPath, 0o440); err != nil {
		return Metadata{}, fmt.Errorf("set asset file permissions: %w", err)
	}

	assetPath := objectPath(tenantID, actualSHA256)
	if err := s.ensureDirectories(filepath.Dir(assetPath)); err != nil {
		return Metadata{}, err
	}
	if err := s.root.Link(tempPath, assetPath); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return Metadata{}, fmt.Errorf("publish immutable asset file: %w", err)
		}
		if err := s.verifyExisting(ctx, assetPath, actualSHA256, sizeBytes); err != nil {
			return Metadata{}, err
		}
	}
	objectDirectory, err := s.root.Open(filepath.ToSlash(filepath.Dir(assetPath)))
	if err != nil {
		return Metadata{}, fmt.Errorf("open asset object directory: %w", err)
	}
	if err := objectDirectory.Sync(); err != nil {
		_ = objectDirectory.Close()
		return Metadata{}, fmt.Errorf("sync asset object directory: %w", err)
	}
	if err := objectDirectory.Close(); err != nil {
		return Metadata{}, fmt.Errorf("close asset object directory: %w", err)
	}
	if err := s.root.Remove(tempPath); err != nil {
		return Metadata{}, fmt.Errorf("remove asset temp link: %w", err)
	}
	keepTemp = false
	return metadata, nil
}

// Verify rehashes stored bytes and rejects an altered or missing object.
func (s *FileStore) Verify(ctx context.Context, tenantID, assetRef, expectedSHA256 string) error {
	if s == nil || s.root == nil {
		return errors.New("asset store is required")
	}
	refTenant, sha256Hex, err := ParseAssetRef(assetRef)
	if err != nil || refTenant != tenantID || validateSHA256(expectedSHA256) != nil || sha256Hex != expectedSHA256 {
		return ErrInvalidAssetRef
	}
	select {
	case s.decodeSlots <- struct{}{}:
		defer func() { <-s.decodeSlots }()
	case <-ctx.Done():
		return ctx.Err()
	}
	return s.verifyExisting(ctx, objectPath(tenantID, sha256Hex), sha256Hex, -1)
}

// Open returns a reader over the stored immutable asset object together with
// its byte size. The stored object is re-verified against the digest before
// any bytes are exposed to the caller; altered objects fail closed.
func (s *FileStore) Open(ctx context.Context, tenantID, sha256Hex string) (io.ReadCloser, int64, error) {
	if s == nil || s.root == nil {
		return nil, 0, errors.New("asset store is required")
	}
	if domain.ValidateUUID32(tenantID) != nil || tenantID != strings.ToLower(tenantID) || validateSHA256(sha256Hex) != nil {
		return nil, 0, ErrInvalidAssetRef
	}
	select {
	case s.decodeSlots <- struct{}{}:
		defer func() { <-s.decodeSlots }()
	case <-ctx.Done():
		return nil, 0, ctx.Err()
	}
	path := objectPath(tenantID, sha256Hex)
	if err := s.verifyExisting(ctx, path, sha256Hex, -1); err != nil {
		return nil, 0, err
	}
	file, err := s.root.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, 0, ErrAssetNotFound
		}
		return nil, 0, fmt.Errorf("open stored asset: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, 0, fmt.Errorf("stat stored asset: %w", err)
	}
	return file, info.Size(), nil
}

// ParseAssetRef validates the canonical tenant-scoped asset reference.
func ParseAssetRef(assetRef string) (tenantID, sha256Hex string, err error) {
	if len(assetRef) != 100 || !strings.HasPrefix(assetRef, "as:") || assetRef[35] != ':' {
		return "", "", ErrInvalidAssetRef
	}
	tenantID = assetRef[3:35]
	sha256Hex = assetRef[36:]
	if domain.ValidateUUID32(tenantID) != nil || strings.ToLower(tenantID) != tenantID || validateSHA256(sha256Hex) != nil {
		return "", "", ErrInvalidAssetRef
	}
	return tenantID, sha256Hex, nil
}

func (s *FileStore) inspect(ctx context.Context, tempPath, sha256Hex string, sizeBytes int64, tenantID string) (Metadata, error) {
	file, err := s.root.Open(tempPath)
	if err != nil {
		return Metadata{}, fmt.Errorf("open uploaded asset: %w", err)
	}
	defer file.Close()
	claimedFormat, err := sniffFormat(file)
	if err != nil {
		return Metadata{}, err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return Metadata{}, fmt.Errorf("rewind uploaded asset: %w", err)
	}
	config, format, err := image.DecodeConfig(file)
	if err != nil {
		return Metadata{}, ErrInvalidImage
	}
	if format != claimedFormat {
		return Metadata{}, ErrInvalidImage
	}
	mimeType, ok := mimeForFormat(format)
	if !ok {
		return Metadata{}, ErrUnsupportedMIME
	}
	if config.Width < 1 || config.Height < 1 || config.Width > MaxDimension || config.Height > MaxDimension {
		return Metadata{}, ErrDimensionsExceeded
	}
	if err := validateStaticImage(file, format, sizeBytes); err != nil {
		return Metadata{}, err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return Metadata{}, fmt.Errorf("rewind uploaded asset: %w", err)
	}
	if _, actualFormat, err := image.Decode(file); err != nil || actualFormat != format {
		return Metadata{}, ErrInvalidImage
	}
	return Metadata{
		AssetRef:  "as:" + tenantID + ":" + sha256Hex,
		SHA256:    sha256Hex,
		SizeBytes: sizeBytes,
		Width:     config.Width,
		Height:    config.Height,
		MIMEType:  mimeType,
	}, nil
}

func sniffFormat(file *os.File) (string, error) {
	var header [12]byte
	if _, err := io.ReadFull(file, header[:]); err != nil {
		return "", ErrInvalidImage
	}
	switch {
	case string(header[:8]) == "\x89PNG\r\n\x1a\n":
		return "png", nil
	case header[0] == 0xff && header[1] == 0xd8 && header[2] == 0xff:
		return "jpeg", nil
	case string(header[0:4]) == "RIFF" && string(header[8:12]) == "WEBP":
		return "webp", nil
	default:
		return "", ErrUnsupportedMIME
	}
}

func (s *FileStore) verifyExisting(ctx context.Context, path, expectedSHA256 string, expectedSize int64) error {
	if err := s.validateDirectories(filepath.ToSlash(filepath.Dir(path))); err != nil {
		return err
	}
	info, err := s.root.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrAssetNotFound
		}
		return fmt.Errorf("inspect stored asset: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return ErrAssetTampered
	}
	if expectedSize >= 0 && info.Size() != expectedSize {
		return ErrAssetTampered
	}
	file, err := s.root.Open(path)
	if err != nil {
		return fmt.Errorf("open stored asset: %w", err)
	}
	defer file.Close()
	hasher := sha256.New()
	limited := &io.LimitedReader{R: file, N: MaxContentBytes + 1}
	actualSize, err := io.Copy(hasher, limited)
	if err != nil {
		return fmt.Errorf("hash stored asset: %w", err)
	}
	if actualSize > MaxContentBytes || actualSize != info.Size() || hex.EncodeToString(hasher.Sum(nil)) != expectedSHA256 {
		return ErrAssetTampered
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func (s *FileStore) ensureDirectories(directory string) error {
	if err := s.root.MkdirAll(directory, 0o750); err != nil {
		return fmt.Errorf("create asset object directories: %w", err)
	}
	parts := strings.Split(filepath.ToSlash(directory), "/")
	current := ""
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		if current == "" {
			current = part
		} else {
			current += "/" + part
		}
		info, err := s.root.Lstat(current)
		if err != nil {
			return fmt.Errorf("inspect asset directory: %w", err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("asset path contains a non-directory component")
		}
	}
	return nil
}

func (s *FileStore) validateDirectories(directory string) error {
	parts := strings.Split(filepath.ToSlash(directory), "/")
	current := ""
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		if current == "" {
			current = part
		} else {
			current += "/" + part
		}
		info, err := s.root.Lstat(current)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return ErrAssetNotFound
			}
			return fmt.Errorf("inspect asset directory: %w", err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return ErrAssetTampered
		}
	}
	return nil
}

func objectPath(tenantID, sha256Hex string) string {
	return filepath.ToSlash(filepath.Join(tenantID, "assets", sha256Hex[:2], sha256Hex[2:4], sha256Hex))
}

func mimeForFormat(format string) (string, bool) {
	switch format {
	case "png":
		return "image/png", true
	case "jpeg":
		return "image/jpeg", true
	case "webp":
		return "image/webp", true
	default:
		return "", false
	}
}

func validateSHA256(value string) error {
	if len(value) != 64 || strings.ToLower(value) != value {
		return ErrInvalidAssetRef
	}
	if _, err := hex.DecodeString(value); err != nil {
		return ErrInvalidAssetRef
	}
	return nil
}

func validateStaticImage(file *os.File, format string, sizeBytes int64) error {
	switch format {
	case "png":
		return validateStaticPNG(file, sizeBytes)
	case "webp":
		return rejectAnimatedWebP(file, sizeBytes)
	default:
		return nil
	}
}

func validateStaticPNG(file *os.File, sizeBytes int64) error {
	if _, err := file.Seek(8, io.SeekStart); err != nil {
		return fmt.Errorf("seek png asset: %w", err)
	}
	for offset := int64(8); offset < sizeBytes; {
		var chunkHeader [8]byte
		if _, err := io.ReadFull(file, chunkHeader[:]); err != nil {
			return ErrInvalidImage
		}
		chunkType := string(chunkHeader[4:8])
		chunkSize := int64(binary.BigEndian.Uint32(chunkHeader[0:4]))
		if chunkType == "acTL" || chunkType == "fcTL" || chunkType == "fdAT" {
			return ErrUnsupportedMIME
		}
		nextOffset := offset + 12 + chunkSize
		if nextOffset > sizeBytes {
			return ErrInvalidImage
		}
		if _, err := file.Seek(nextOffset, io.SeekStart); err != nil {
			return ErrInvalidImage
		}
		offset = nextOffset
		if chunkType == "IEND" {
			if chunkSize != 0 || offset != sizeBytes {
				return ErrInvalidImage
			}
			return nil
		}
	}
	return ErrInvalidImage
}

func rejectAnimatedWebP(file *os.File, sizeBytes int64) error {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind webp asset: %w", err)
	}
	var header [12]byte
	if _, err := io.ReadFull(file, header[:]); err != nil || string(header[0:4]) != "RIFF" || string(header[8:12]) != "WEBP" {
		return ErrInvalidImage
	}
	riffSize := int64(binary.LittleEndian.Uint32(header[4:8])) + 8
	if riffSize != sizeBytes || riffSize < 12 {
		return ErrInvalidImage
	}
	for offset := int64(12); offset < riffSize; {
		var chunkHeader [8]byte
		if _, err := io.ReadFull(file, chunkHeader[:]); err != nil {
			return ErrInvalidImage
		}
		chunkType := string(chunkHeader[0:4])
		chunkSize := int64(binary.LittleEndian.Uint32(chunkHeader[4:8]))
		if chunkType == "ANIM" || chunkType == "ANMF" {
			return ErrUnsupportedMIME
		}
		nextOffset := offset + 8 + chunkSize + chunkSize%2
		if chunkSize < 0 || nextOffset > riffSize {
			return ErrInvalidImage
		}
		if _, err := file.Seek(nextOffset, io.SeekStart); err != nil {
			return ErrInvalidImage
		}
		offset = nextOffset
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind webp asset: %w", err)
	}
	return nil
}
