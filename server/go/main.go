// Minimal TLTV node — Go 1.22+, zero dependencies
// Usage: go run main.go

package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const b58alpha = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

func b58encode(data []byte) string {
	n := new(big.Int).SetBytes(data)
	mod, zero, base := new(big.Int), new(big.Int), big.NewInt(58)
	var r []byte
	for n.Cmp(zero) > 0 {
		n.DivMod(n, base, mod)
		r = append(r, b58alpha[mod.Int64()])
	}
	for _, b := range data {
		if b != 0 {
			break
		}
		r = append(r, b58alpha[0])
	}
	for i, j := 0, len(r)-1; i < j; i, j = i+1, j-1 {
		r[i], r[j] = r[j], r[i]
	}
	return string(r)
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	port := env("PORT", "8000")
	name := env("CHANNEL_NAME", "Go TLTV Channel")
	media := env("MEDIA_DIR", "./media")

	priv, pub := loadOrCreateKey("channel.key")
	id := b58encode(append([]byte{0x14, 0x33}, pub...))
	pfx := "/tltv/v1/channels/" + id

	signMeta := func() map[string]any {
		doc := map[string]any{
			"v": 1, "seq": time.Now().Unix(),
			"id": id, "name": name,
			"stream":  pfx + "/stream.m3u8",
			"updated": time.Now().UTC().Format("2006-01-02T15:04:05Z"),
		}
		payload, _ := json.Marshal(doc)
		doc["signature"] = b58encode(ed25519.Sign(priv, payload))
		return doc
	}

	mux := http.NewServeMux()

	mux.HandleFunc("GET /.well-known/tltv", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"protocol": "tltv", "versions": []int{1},
			"channels": []map[string]string{{"id": id, "name": name}}, "relaying": []any{}})
	})
	mux.HandleFunc("GET /tltv/v1/channels/{cid}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("cid") != id {
			writeErr(w, 404, "channel_not_found")
			return
		}
		writeJSON(w, signMeta())
	})
	mux.HandleFunc("GET /tltv/v1/channels/{cid}/stream.m3u8", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("cid") != id {
			writeErr(w, 404, "channel_not_found")
			return
		}
		p := filepath.Join(media, "stream.m3u8")
		if _, err := os.Stat(p); err != nil {
			writeErr(w, 503, "stream_unavailable")
			return
		}
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Header().Set("Cache-Control", "max-age=1, no-cache")
		http.ServeFile(w, r, p)
	})
	mux.HandleFunc("GET /tltv/v1/channels/{cid}/{file}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("cid") != id {
			writeErr(w, 404, "channel_not_found")
			return
		}
		f := r.PathValue("file")
		if !strings.HasSuffix(f, ".ts") {
			writeErr(w, 404, "channel_not_found")
			return
		}
		p := filepath.Join(media, filepath.Base(f))
		if _, err := os.Stat(p); err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "max-age=3600")
		http.ServeFile(w, r, p)
	})
	mux.HandleFunc("GET /tltv/v1/peers", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"peers": []any{}})
	})

	fmt.Printf("Channel: %s\nListen:  :%s\nURI:     tltv://%s@localhost:%s\n\nVerify:  tltv fetch %s@localhost:%s\n", id, port, id, port, id, port)
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

func loadOrCreateKey(path string) (ed25519.PrivateKey, ed25519.PublicKey) {
	if data, err := os.ReadFile(path); err == nil {
		s := strings.TrimSpace(string(data))
		var seed []byte
		if len(s) == 64 {
			seed, _ = hex.DecodeString(s)
		}
		if seed == nil && len(data) == 32 {
			seed = data
		}
		if len(seed) == 32 {
			priv := ed25519.NewKeyFromSeed(seed)
			return priv, priv.Public().(ed25519.PublicKey)
		}
	}
	pub, priv, _ := ed25519.GenerateKey(nil)
	os.WriteFile(path, []byte(hex.EncodeToString(priv.Seed())+"\n"), 0600)
	fmt.Println("Generated new keypair -> channel.key")
	return priv, pub
}
