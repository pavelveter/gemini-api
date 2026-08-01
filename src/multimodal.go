package main

import (
	"bytes"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

var pageTokenPatterns = map[string]*regexp.Regexp{
	"push_id": regexp.MustCompile(`"qKIAYe":"([^"]+)"`),
	"pctx":    regexp.MustCompile(`"Ylro7b":"([^"]+)"`),
	"at":      regexp.MustCompile(`"thykhd":"([^"]+)"`),
}

var (
	pageTokensMu    sync.Mutex
	pageTokens      = map[string]string{}
	pageTokensSince time.Time
)

// getPageTokens fetches WIZ_global_data tokens from the Gemini page
// (Push-ID, X-Client-Pctx).
func getPageTokens() map[string]string {
	req, _ := http.NewRequest(http.MethodGet, geminiWebBase+"/app", nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	cookieStr, _ := loadCookie()
	if cookieStr != "" {
		req.Header.Set("Cookie", cookieStr)
	}
	resp, err := getHTTPClient().Do(req)
	if err != nil {
		log("Page token fetch failed: " + err.Error())
		return map[string]string{}
	}
	defer resp.Body.Close()
	html, err := readAllTimeout(resp.Body, 30*time.Second)
	if err != nil {
		log("Page token fetch failed: " + err.Error())
		return map[string]string{}
	}
	tokens := map[string]string{}
	for key, re := range pageTokenPatterns {
		if m := re.FindStringSubmatch(string(html)); m != nil {
			tokens[key] = m[1]
		}
	}
	return tokens
}

// cachedPageTokens returns page tokens with a 10-minute cache. The lock is
// released during the network fetch so concurrent uploads are not blocked.
func cachedPageTokens() map[string]string {
	pageTokensMu.Lock()
	expired := time.Since(pageTokensSince) > 600*time.Second
	if !expired {
		tokens := pageTokens
		pageTokensMu.Unlock()
		return tokens
	}
	pageTokensMu.Unlock()

	fresh := getPageTokens()

	pageTokensMu.Lock()
	pageTokens = fresh
	pageTokensSince = time.Now()
	tokens := pageTokens
	pageTokensMu.Unlock()
	return tokens
}

// uploadImage uploads an image via the Scotty resumable upload protocol and
// returns the file reference path.
func uploadImage(imageBytes []byte, filename, mimeType string) (string, error) {
	tokens := cachedPageTokens()
	pushID := tokens["push_id"]
	if pushID == "" {
		pushID = "feeds/mcudyrk2a4khkz"
	}
	pctx := tokens["pctx"]
	if pctx == "" {
		pctx = "CgcSBWjK7pYx"
	}
	cookieStr, sapisid := loadCookie()
	client := getHTTPClient()

	// Step 1: initiate resumable upload session.
	startReq, _ := http.NewRequest(http.MethodPost, "https://content-push.googleapis.com/upload/", strings.NewReader(""))
	startReq.Header.Set("Push-ID", pushID)
	startReq.Header.Set("X-Tenant-Id", "bard-storage")
	startReq.Header.Set("X-Client-Pctx", pctx)
	startReq.Header.Set("X-Goog-Upload-Header-Content-Length", strconv.Itoa(len(imageBytes)))
	startReq.Header.Set("X-Goog-Upload-Header-Content-Type", mimeType)
	startReq.Header.Set("X-Goog-Upload-Protocol", "resumable")
	startReq.Header.Set("X-Goog-Upload-Command", "start")
	startReq.Header.Set("Content-Type", "application/x-www-form-urlencoded;charset=utf-8")
	startReq.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	if cookieStr != "" {
		startReq.Header.Set("Cookie", cookieStr)
	}
	if sapisid != "" {
		startReq.Header.Set("Authorization", makeSapisidhash(sapisid))
	}
	startResp, err := client.Do(startReq)
	if err != nil {
		return "", err
	}
	_, _ = readAllTimeout(startResp.Body, 30*time.Second)
	startResp.Body.Close()

	uploadURL := startResp.Header.Get("X-Goog-Upload-URL")
	if uploadURL == "" {
		return "", fmt.Errorf("no upload URL in response headers")
	}
	log("Upload session started: " + truncate(uploadURL, 80) + "...")

	// Step 2: upload file data and finalize.
	upReq, _ := http.NewRequest(http.MethodPost, uploadURL, bytes.NewReader(imageBytes))
	upReq.Header.Set("X-Goog-Upload-Command", "upload, finalize")
	upReq.Header.Set("X-Goog-Upload-Offset", "0")
	upReq.Header.Set("Content-Type", "application/octet-stream")
	upReq.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	upResp, err := client.Do(upReq)
	if err != nil {
		return "", err
	}
	defer upResp.Body.Close()
	body, err := readAllTimeout(upResp.Body, 60*time.Second)
	if err != nil {
		return "", err
	}
	fileRef := strings.TrimSpace(string(body))
	if fileRef == "" || !strings.HasPrefix(fileRef, "/") {
		return "", fmt.Errorf("invalid file reference: %s", truncate(fileRef, 100))
	}
	log("Image uploaded: " + filename + " -> " + truncate(fileRef, 50) + "...")
	return fileRef, nil
}

// fetchImageBytes downloads an image from a URL.
func fetchImageBytes(rawURL string) []byte {
	req, _ := http.NewRequest(http.MethodGet, rawURL, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := getHTTPClient().Do(req)
	if err != nil {
		log("Image fetch failed: " + err.Error())
		return nil
	}
	defer resp.Body.Close()
	data, err := readAllTimeout(resp.Body, 30*time.Second)
	if err != nil {
		log("Image fetch failed: " + err.Error())
		return nil
	}
	return data
}

// uploadImages uploads a list of images and returns file references, or nil
// when there are no images / all uploads failed.
func uploadImages(images []imageInput) []string {
	if len(images) == 0 {
		return nil
	}
	var refs []string
	for _, item := range images {
		data := item.Data
		mime := item.Mime
		if len(data) == 0 && item.URL != "" {
			data = fetchImageBytes(item.URL)
			if mime == "" {
				mime = "image/png"
			}
		}
		if len(data) > 0 {
			if mime == "" {
				mime = "image/png"
			}
			ref, err := uploadImage(data, "image.png", mime)
			if err != nil {
				log("Image upload failed: " + err.Error())
			} else {
				refs = append(refs, ref)
			}
		}
	}
	if len(refs) == 0 {
		return nil
	}
	return refs
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
