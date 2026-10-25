package dash

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Screenshots dropped or pasted into the send box. A pane takes only text,
// so each image is saved on this machine and its path goes with the
// message, as Herdr's own clipboard paste does; the agent opens the file.

const (
	maxImages     = 6
	maxImageBytes = 10 << 20
	// imagesKeep is how long a saved image stays before it is cleared.
	imagesKeep = 7 * 24 * time.Hour
)

// imageExt names the image kinds taken, by sniffed content type.
var imageExt = map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/gif": ".gif", "image/webp": ".webp"}

func (s *webServer) uploadDir() string {
	if s.src.UploadDir != "" {
		return s.src.UploadDir
	}
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "goagentic", "uploads")
}

// saveImages saves the request's images and returns their paths.
func (s *webServer) saveImages(r *http.Request) ([]string, error) {
	if r.MultipartForm == nil {
		return nil, nil
	}
	files := r.MultipartForm.File["image"]
	if len(files) > maxImages {
		return nil, fmt.Errorf("at most %d images per message", maxImages)
	}
	if len(files) == 0 {
		return nil, nil
	}
	dir := s.uploadDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	clearOldImages(dir)
	var paths []string
	for _, fh := range files {
		path, err := saveImage(dir, fh)
		if err != nil {
			return nil, err
		}
		paths = append(paths, path)
	}
	return paths, nil
}

func saveImage(dir string, fh *multipart.FileHeader) (string, error) {
	if fh.Size > maxImageBytes {
		return "", fmt.Errorf("%s is larger than %d MB", fh.Filename, maxImageBytes>>20)
	}
	f, err := fh.Open()
	if err != nil {
		return "", err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxImageBytes+1))
	if err != nil {
		return "", err
	}
	ext, ok := imageExt[http.DetectContentType(data)]
	if !ok || len(data) > maxImageBytes {
		return "", errors.New(fh.Filename + " is not a PNG, JPEG, GIF or WebP image")
	}
	id := make([]byte, 4)
	_, _ = rand.Read(id)
	path := filepath.Join(dir, time.Now().Format("20060102-150405")+"-"+hex.EncodeToString(id)+ext)
	return path, os.WriteFile(path, data, 0o600)
}

// clearOldImages removes saved images older than imagesKeep.
func clearOldImages(dir string) {
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if info, err := e.Info(); err == nil && !e.IsDir() && time.Since(info.ModTime()) > imagesKeep {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// withImages adds the saved images' paths to a message.
func withImages(text string, paths []string) string {
	var b strings.Builder
	b.WriteString(text)
	for _, p := range paths {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("[Image: " + p + "]")
	}
	return b.String()
}

// imageLineRe is a line withImages wrote.
var imageLineRe = regexp.MustCompile(`^\[Image: (.+)\]$`)

// chatImages takes the lines naming images saved here out of a sent
// message, and returns the rest and the images' page links.
func (s *webServer) chatImages(text string) (string, []string) {
	if !strings.Contains(text, "[Image: ") {
		return text, nil
	}
	dir := s.uploadDir()
	var keep, links []string
	for _, line := range strings.Split(text, "\n") {
		if m := imageLineRe.FindStringSubmatch(strings.TrimSpace(line)); m != nil && filepath.Dir(m[1]) == dir {
			links = append(links, "/uploads/"+filepath.Base(m[1]))
			continue
		}
		keep = append(keep, line)
	}
	return strings.TrimSpace(strings.Join(keep, "\n")), links
}

// upload serves an image saved from the send box, by its bare name.
func (s *webServer) upload(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name != filepath.Base(name) || strings.HasPrefix(name, ".") || imageExtOf(name) == "" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeFile(w, r, filepath.Join(s.uploadDir(), name))
}

func imageExtOf(name string) string {
	ext := filepath.Ext(name)
	for _, e := range imageExt {
		if e == ext {
			return e
		}
	}
	return ""
}
