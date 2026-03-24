// Minimal TLTV relay — Go 1.22+, zero dependencies
// Usage: UPSTREAM=localhost:8000 go run main.go

package main

import (
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const b58alpha = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

func b58decode(s string) []byte {
	n := new(big.Int)
	for _, c := range s {
		n.Mul(n, big.NewInt(58))
		n.Add(n, big.NewInt(int64(strings.IndexRune(b58alpha, c))))
	}
	b := n.Bytes()
	for i := 0; i < len(s) && s[i] == '1'; i++ {
		b = append([]byte{0}, b...)
	}
	return b
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// verifyDoc checks an Ed25519 signature on a TLTV metadata document.
// Returns the parsed fields and raw bytes; no private key needed.
func verifyDoc(raw []byte) (map[string]json.RawMessage, bool) {
	var doc map[string]json.RawMessage
	if json.Unmarshal(raw, &doc) != nil {
		return nil, false
	}
	var id, sig string
	if json.Unmarshal(doc["id"], &id) != nil || json.Unmarshal(doc["signature"], &sig) != nil {
		return nil, false
	}
	idBytes := b58decode(id)
	if len(idBytes) < 34 || idBytes[0] != 0x14 || idBytes[1] != 0x33 {
		return nil, false
	}
	body := make(map[string]json.RawMessage)
	for k, v := range doc {
		if k != "signature" {
			body[k] = v
		}
	}
	canonical, err := json.Marshal(body) // Go sorts map keys alphabetically
	if err != nil {
		return nil, false
	}
	return doc, ed25519.Verify(idBytes[2:], canonical, b58decode(sig))
}

// Relay state
type channel struct {
	metaRaw  []byte                     // verbatim JSON from upstream (served as-is)
	meta     map[string]json.RawMessage // parsed for field access
	manifest []byte
	segs     map[string][]byte
}

var (
	mu       sync.RWMutex
	channels = map[string]*channel{}
	upURL    string
	client   = &http.Client{Timeout: 5 * time.Second}
)

func upGet(path string) ([]byte, error) {
	resp, err := client.Get(upURL + path)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func metaStr(doc map[string]json.RawMessage, key string) string {
	var s string
	json.Unmarshal(doc[key], &s)
	return s
}

func metaInt(doc map[string]json.RawMessage, key string) int64 {
	var n int64
	json.Unmarshal(doc[key], &n)
	return n
}

func metaBool(doc map[string]json.RawMessage, key string) bool {
	var b bool
	json.Unmarshal(doc[key], &b)
	return b
}

func syncMeta() {
	data, err := upGet("/.well-known/tltv")
	if err != nil {
		fmt.Fprintf(os.Stderr, "  sync: %v\n", err)
		return
	}
	var info struct {
		Channels []struct {
			ID string `json:"id"`
		} `json:"channels"`
		Relaying []struct {
			ID string `json:"id"`
		} `json:"relaying"`
	}
	if json.Unmarshal(data, &info) != nil {
		return
	}
	seen := map[string]bool{}
	for _, entry := range append(info.Channels, info.Relaying...) {
		raw, err := upGet("/tltv/v1/channels/" + entry.ID)
		if err != nil {
			continue
		}
		doc, ok := verifyDoc(raw)
		if !ok {
			continue
		}
		if metaStr(doc, "access") == "token" || metaBool(doc, "on_demand") {
			continue
		}
		seen[entry.ID] = true
		mu.Lock()
		if ch, exists := channels[entry.ID]; !exists {
			channels[entry.ID] = &channel{metaRaw: raw, meta: doc, segs: map[string][]byte{}}
			fmt.Printf("  + %s (%s)\n", metaStr(doc, "name"), entry.ID)
		} else if metaInt(doc, "seq") > metaInt(ch.meta, "seq") {
			if metaStr(doc, "status") == "retired" {
				delete(channels, entry.ID)
			} else {
				ch.metaRaw = raw
				ch.meta = doc
			}
		}
		mu.Unlock()
	}
	mu.Lock()
	for id := range channels {
		if !seen[id] {
			delete(channels, id)
		}
	}
	mu.Unlock()
}

func syncHLS() {
	mu.RLock()
	ids := make([]string, 0, len(channels))
	for id := range channels {
		ids = append(ids, id)
	}
	mu.RUnlock()

	for _, id := range ids {
		data, err := upGet("/tltv/v1/channels/" + id + "/stream.m3u8")
		if err != nil {
			continue
		}
		names := map[string]bool{}
		for _, line := range strings.Split(string(data), "\n") {
			name := strings.TrimSpace(line)
			if name != "" && !strings.HasPrefix(name, "#") {
				names[name] = true
			}
		}
		// Determine which segments to fetch (read lock only)
		mu.RLock()
		ch := channels[id]
		if ch == nil {
			mu.RUnlock()
			continue
		}
		var toFetch []string
		for name := range names {
			if _, ok := ch.segs[name]; !ok {
				toFetch = append(toFetch, name)
			}
		}
		mu.RUnlock()

		// Fetch new segments without holding any lock
		fetched := map[string][]byte{}
		for _, name := range toFetch {
			if seg, err := upGet("/tltv/v1/channels/" + id + "/" + name); err == nil {
				fetched[name] = seg
			}
		}

		// Update cache
		mu.Lock()
		if ch = channels[id]; ch != nil {
			ch.manifest = data
			for name, seg := range fetched {
				ch.segs[name] = seg
			}
			for k := range ch.segs {
				if !names[k] {
					delete(ch.segs, k)
				}
			}
		}
		mu.Unlock()
	}
}

func main() {
	port := env("PORT", "9000")
	upURL = "http://" + env("UPSTREAM", "localhost:8000")

	mux := http.NewServeMux()

	mux.HandleFunc("GET /.well-known/tltv", func(w http.ResponseWriter, r *http.Request) {
		mu.RLock()
		relaying := make([]map[string]string, 0, len(channels))
		for _, ch := range channels {
			relaying = append(relaying, map[string]string{
				"id": metaStr(ch.meta, "id"), "name": metaStr(ch.meta, "name"),
			})
		}
		mu.RUnlock()
		writeJSON(w, map[string]any{"protocol": "tltv", "versions": []int{1},
			"channels": []any{}, "relaying": relaying})
	})
	mux.HandleFunc("GET /tltv/v1/peers", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"peers": []any{}})
	})
	mux.HandleFunc("GET /tltv/v1/channels/{cid}", func(w http.ResponseWriter, r *http.Request) {
		mu.RLock()
		ch := channels[r.PathValue("cid")]
		var raw []byte
		if ch != nil {
			raw = ch.metaRaw
		}
		mu.RUnlock()
		if ch == nil {
			writeErr(w, 404, "channel_not_found")
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Write(raw)
	})
	mux.HandleFunc("GET /tltv/v1/channels/{cid}/stream.m3u8", func(w http.ResponseWriter, r *http.Request) {
		mu.RLock()
		ch := channels[r.PathValue("cid")]
		var manifest []byte
		if ch != nil {
			manifest = ch.manifest
		}
		mu.RUnlock()
		if ch == nil {
			writeErr(w, 404, "channel_not_found")
			return
		}
		if manifest == nil {
			writeErr(w, 503, "stream_unavailable")
			return
		}
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Header().Set("Cache-Control", "max-age=1, no-cache")
		w.Write(manifest)
	})
	mux.HandleFunc("GET /tltv/v1/channels/{cid}/{file}", func(w http.ResponseWriter, r *http.Request) {
		f := r.PathValue("file")
		if !strings.HasSuffix(f, ".ts") {
			writeErr(w, 404, "channel_not_found")
			return
		}
		mu.RLock()
		ch := channels[r.PathValue("cid")]
		var seg []byte
		if ch != nil {
			seg = ch.segs[f]
		}
		mu.RUnlock()
		if ch == nil {
			writeErr(w, 404, "channel_not_found")
			return
		}
		if seg == nil {
			writeErr(w, 503, "stream_unavailable")
			return
		}
		w.Header().Set("Content-Type", "video/mp2t")
		w.Header().Set("Cache-Control", "max-age=3600")
		w.Write(seg)
	})

	fmt.Printf("TLTV Relay  upstream=%s\nListen:  :%s\n\nVerify:  tltv node localhost:%s --local\n",
		env("UPSTREAM", "localhost:8000"), port, port)

	go func() {
		syncMeta()
		metaTick := time.NewTicker(60 * time.Second)
		hlsTick := time.NewTicker(2 * time.Second)
		for {
			select {
			case <-metaTick.C:
				syncMeta()
			case <-hlsTick.C:
				syncHLS()
			}
		}
	}()

	http.ListenAndServe(":"+port, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		if r.Method == "OPTIONS" {
			w.WriteHeader(204)
			return
		}
		mux.ServeHTTP(w, r)
	}))
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
