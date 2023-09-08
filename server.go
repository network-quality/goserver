// Copyright (c) 2021-2023 Apple Inc. Licensed under MIT License.

package goserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"strconv"
)

const (
	smallContentLength int64 = 1
	largeContentLength int64 = 8 * 1024 * 1024 * 1024
	chunkSize          int64 = 64 * 1024
	maxPeriodicSize    int64 = 4 * 1024

	// ServiceType is the dns-sd service type for this service
	ServiceType = "_nq._tcp"
)

var (
	chunkBuffed    []byte
	periodicBuffed []byte
)

func init() {
	chunkBuffed = make([]byte, chunkSize)
	for i := range chunkBuffed {
		chunkBuffed[i] = 'x'
	}
	periodicBuffed = make([]byte, maxPeriodicSize)
	for i := range periodicBuffed {
		periodicBuffed[i] = 'x'
	}
}

// setCors makes it possible for wasm clients to connect to the server
// from a webclient that is not hosted on the same domain.
func setCors(h http.Header) {
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Access-Control-Allow-Headers", "*")
}

type handlers struct {
	EnableCORS    bool
	BytesServed   *uint64
	BytesReceived *uint64
}

// BulkHandlers returns path, handler tuples with the provided prefix.
func BulkHandlers(prefix string, EnableCORS bool) map[string]http.HandlerFunc {
	h := &handlers{EnableCORS: EnableCORS}
	return map[string]http.HandlerFunc{
		prefix + "/small":    h.smallHandler,
		prefix + "/large":    h.largeHandler,
		prefix + "/slurp":    h.slurpHandler,
		prefix + "/periodic": h.periodicHandler,
	}
}

func CountingBulkHandlers(prefix string, EnableCORS bool, bytesServed, bytesReceived *uint64) map[string]http.HandlerFunc {
	h := &handlers{EnableCORS: EnableCORS, BytesServed: bytesServed, BytesReceived: bytesReceived}
	return map[string]http.HandlerFunc{
		prefix + "/small":    h.smallHandler,
		prefix + "/large":    h.largeHandler,
		prefix + "/slurp":    h.slurpHandler,
		prefix + "/periodic": h.periodicHandler,
	}
}

// A Server defines parameters for running a network quality server.
type Server struct {
	PublicPort     int
	PublicHostPort string
	ContextPath    string
	Scheme         string
	EnableCORS     bool
	EnableH3AltSvc bool
	BytesServed    uint64
	BytesReceived  uint64

	generatedConfig []byte
	once            sync.Once
}

func (m *Server) PrintStats() {
	var lastBytesServed uint64
	var lastBytesReceived uint64
	for {
		var msg string
		x := atomic.LoadUint64(&m.BytesServed)
		y := atomic.LoadUint64(&m.BytesReceived)

		if x > lastBytesServed {
			dur := time.Second
			delta := x - lastBytesServed
			bps := float64(delta) / dur.Seconds()
			throughput := (bps / float64(1024*1024) * 8)

			msg += fmt.Sprintf("Sent: %0.2fMbps", throughput)
			lastBytesServed = x
		}

		if y > lastBytesReceived {
			dur := time.Second
			delta := y - lastBytesReceived
			bps := float64(delta) / dur.Seconds()
			throughput := (bps / float64(1024*1024) * 8)
			if len(msg) > 0 {
				msg += " "
			}
			msg += fmt.Sprintf("Received: %0.2fMbps", throughput)
			lastBytesReceived = y
		}

		if len(msg) > 0 {
			log.Printf("%s", msg)
		}
		time.Sleep(1 * time.Second)
	}
}

func (m *Server) generateConfig() {
	urls := struct {
		SmallDownloadURL      string `json:"small_download_url"`
		LargeDownloadURL      string `json:"large_download_url"`
		UploadURL             string `json:"upload_url"`
		SmallHTTPSDownloadURL string `json:"small_https_download_url"`
		LargeHTTPSDownloadURL string `json:"large_https_download_url"`
		HTTPSUploadURL        string `json:"https_upload_url"`
		HTTPSPeriodicURL      string `json:"https_periodic_url"`
	}{
		SmallDownloadURL:      m.generateSmallDownloadURL(),
		LargeDownloadURL:      m.generateLargeDownloadURL(),
		UploadURL:             m.generateUploadURL(),
		SmallHTTPSDownloadURL: m.generateSmallDownloadURL(),
		LargeHTTPSDownloadURL: m.generateLargeDownloadURL(),
		HTTPSUploadURL:        m.generateUploadURL(),
		HTTPSPeriodicURL:      m.generatePeriodicURL(),
	}

	resp := struct {
		Version int         `json:"version"`
		Urls    interface{} `json:"urls"`
	}{
		Version: 1,
		Urls:    urls,
	}

	b, err := json.MarshalIndent(resp, "", "    ")
	if err != nil {
		log.Fatal(err)
	}

	m.generatedConfig = b
}

func (m *Server) ConfigHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	m.once.Do(func() { m.generateConfig() })

	w.Header().Set("Content-Type", "application/json")
	if m.EnableH3AltSvc {
		w.Header().Set("Alt-Svc", fmt.Sprintf("h3=\":%d\"", m.PublicPort))
	}

	if m.EnableCORS {
		setCors(w.Header())
	}

	_, err := w.Write(m.generatedConfig)
	if err != nil {
		log.Printf("could not write response: %s", err)
	}
}

func (m *Server) generateSmallDownloadURL() string {
	return fmt.Sprintf("%s://%s%s/small", m.Scheme, m.PublicHostPort, m.ContextPath)
}

func (m *Server) generateLargeDownloadURL() string {
	return fmt.Sprintf("%s://%s%s/large", m.Scheme, m.PublicHostPort, m.ContextPath)
}

func (m *Server) generateUploadURL() string {
	return fmt.Sprintf("%s://%s%s/slurp", m.Scheme, m.PublicHostPort, m.ContextPath)
}

func (m *Server) generatePeriodicURL() string {
	return fmt.Sprintf("%s://%s%s/periodic", m.Scheme, m.PublicHostPort, m.ContextPath)
}

func (h *handlers) smallHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Length", strconv.FormatInt(smallContentLength, 10))
	w.Header().Set("Content-Type", "application/octet-stream")

	if h.EnableCORS {
		setCors(w.Header())
	}

	if err := h.chunkedBodyWriter(w, smallContentLength); !ignorableError(err) {
		log.Printf("Error writing content of length %d: %s", smallContentLength, err)
	}
}

func (h *handlers) largeHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Length", strconv.FormatInt(largeContentLength, 10))
	w.Header().Set("Content-Type", "application/octet-stream")

	if h.EnableCORS {
		setCors(w.Header())
	}

	if r.Method != http.MethodGet {
		return
	}

	if err := h.chunkedBodyWriter(w, largeContentLength); !ignorableError(err) {
		log.Printf("Error writing content of length %d: %s", largeContentLength, err)
	}
}

func (h *handlers) periodicHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !r.URL.Query().Has("size") {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	periodicSize, err := strconv.ParseInt(r.URL.Query().Get("size"), 10, 64)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Length", strconv.FormatInt(largeContentLength, 10))
	w.Header().Set("Content-Type", "application/octet-stream")

	if h.EnableCORS {
		setCors(w.Header())
	}

	if r.Method != http.MethodGet {
		return
	}

	if err := h.periodicBodyWriter(w, time.Second, periodicSize, largeContentLength); !ignorableError(err) {
		log.Printf("Error periodically writing content of length %d: %s", largeContentLength, err)
	}
}

func (h *handlers) chunkedBodyWriter(w http.ResponseWriter, contentLength int64) error {
	w.WriteHeader(http.StatusOK)

	n := contentLength
	for n > 0 {
		if n >= chunkSize {
			n -= chunkSize
			atomic.AddUint64(h.BytesServed, uint64(chunkSize))

			if _, err := w.Write(chunkBuffed); err != nil {
				return err
			}
			continue
		}

		atomic.AddUint64(h.BytesServed, uint64(n))
		if _, err := w.Write(chunkBuffed[:n]); err != nil {
			return err
		}
		break
	}

	return nil
}

func (h *handlers) periodicBodyWriter(w http.ResponseWriter, interval time.Duration, periodicSize int64, contentLength int64) error {
	var err error = nil
	w.WriteHeader(http.StatusOK)

	periodicWriterWg := sync.WaitGroup{}

	periodicSize = int64(math.Min(float64(periodicSize), float64(maxPeriodicSize)))

	periodicWriterWg.Add(1)
	go func() {
		n := contentLength
		for n > 0 {
			// Send a _periodicSize_ amount of data every _interval_ period.
			time.Sleep(interval)

			// n >= chunkSize: We have at least a full _periodicSize_ amount of data left to send.
			if n >= periodicSize {
				n -= periodicSize
				atomic.AddUint64(h.BytesServed, uint64(periodicSize))

				if _, writeErr := w.Write(periodicBuffed[:periodicSize]); writeErr != nil {
					err = writeErr
					break
				}
				// Because we are attempting to detect buffer bloat, as soon
				// as we have written data, we flush it to the network.
				// In other words, we can't very well detect bloat if *we* are
				// the source of it!
				flusher, ok := w.(http.Flusher)
				if ok {
					flusher.Flush()
				} else {
					log.Printf("The http response writer is not flushable -- abnormal situation")
				}
				continue
			}

			// n < chunkSize: We have less than a full chunk left to send
			atomic.AddUint64(h.BytesServed, uint64(n))
			if _, writeErr := w.Write(periodicBuffed[:n]); writeErr != nil {
				err = writeErr
			}
			break
		}
		periodicWriterWg.Done()
	}()

	periodicWriterWg.Wait()

	return err
}

// setNoPublicCache tells the proxy to cache the content and the user
// that it can't be cached. It requires the proxy cache to be configured
// to use the Proxy-Cache-Control header
func setNoPublicCache(h http.Header) {
	h.Set("Proxy-Cache-Control", "max-age=604800, public")
	h.Set("Cache-Control", "no-store, must-revalidate, private, max-age=0")
}

// slurpHandler reads the post request and returns JSON with bytes
// read and how long it took
func (h *handlers) slurpHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/octet-stream")
	setNoPublicCache(w.Header())

	if h.EnableCORS {
		setCors((w.Header()))
	}

	_, err := io.Copy(countingDiscard{byteCounter: h.BytesReceived}, r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// countingDiscard implements ReaderFrom as an optimization so Copy to
// io.Discard can avoid doing unnecessary work.
// Modified from go's io/io.go discard to count the number of bytes discarded
type countingDiscard struct {
	byteCounter *uint64
}

func (cb countingDiscard) Write(p []byte) (int, error) {
	x := len(p)
	return x, nil
}

func (cb countingDiscard) WriteString(s string) (int, error) {
	x := len(s)
	return x, nil
}

var blackHolePool = sync.Pool{
	New: func() any {
		b := make([]byte, 8192)
		return &b
	},
}

func (cb countingDiscard) ReadFrom(r io.Reader) (n int64, err error) {
	bufp := blackHolePool.Get().(*[]byte)
	var readSize int
	for {
		readSize, err = r.Read(*bufp)
		n += int64(readSize)
		atomic.AddUint64(cb.byteCounter, uint64(readSize))
		if err != nil {
			blackHolePool.Put(bufp)
			if err == io.EOF {
				return n, nil
			}
			return
		}
	}
}

// ignorableError returns true if error does not effect results of clients accessing server
func ignorableError(err error) bool {
	if err == nil {
		return true
	}
	if errors.Is(err, syscall.EPIPE) {
		return true
	}
	if errors.Is(err, syscall.ECONNRESET) {
		return true
	}

	switch err.Error() {
	case "client disconnected": // from http.http2errClientDisconnected
		return true
	}
	return false
}
