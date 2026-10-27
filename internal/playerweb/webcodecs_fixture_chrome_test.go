// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package playerweb

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	goruntime "runtime"

	"github.com/chromedp/chromedp"
)

func TestWebCodecs_FixtureAVCC_InChrome(t *testing.T) {
	chromePath := findChrome(t)
	_, file, _, ok := goruntime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	fixture := filepath.Join(filepath.Dir(file), "testdata", "baseline_320x240_60fps.h264")
	raw, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	aus, err := annexBAccessUnits(raw)
	if err != nil {
		t.Fatal(err)
	}
	avcc, err := annexBToAVCC(aus[0])
	if err != nil {
		t.Fatal(err)
	}
	deltaAVCC, err := annexBToAVCC(aus[1])
	if err != nil {
		t.Fatal(err)
	}
	keyB64 := base64.StdEncoding.EncodeToString(avcc)
	deltaB64 := base64.StdEncoding.EncodeToString(deltaAVCC)
	t.Logf("fixture avcc key=%d delta=%d bytes", len(avcc), len(deltaAVCC))

	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:],
			chromedp.ExecPath(chromePath),
			chromedp.Flag("headless", true),
			chromedp.Flag("disable-gpu", true),
		)...)
	defer cancelAlloc()
	browser, cancelBrowser := chromedp.NewContext(allocCtx)
	defer cancelBrowser()
	ctx, cancel := context.WithTimeout(browser, 30*time.Second)
	defer cancel()

	kickoff := `(async () => {
  globalThis.__wcFixture = 'pending';
  try {
  const avccToAnnexB = (a) => {
    const out = [];
    for (let i = 0; i + 4 <= a.length;) {
      const len = (a[i]<<24)|(a[i+1]<<16)|(a[i+2]<<8)|a[i+3];
      i += 4;
      if (len <= 0 || i + len > a.length) break;
      if ((a[i] & 0x1f) !== 6) {
        out.push(0,0,0,1);
        for (let j = 0; j < len; j++) out.push(a[i+j]);
      }
      i += len;
    }
    return new Uint8Array(out);
  };
  const codecFromAVCC = (a) => {
    for (let i = 0; i + 4 <= a.length;) {
      const len = (a[i]<<24)|(a[i+1]<<16)|(a[i+2]<<8)|a[i+3];
      i += 4;
      if (i + len > a.length || len < 4) break;
      if ((a[i] & 0x1f) === 7) {
        const h = (n) => n.toString(16).padStart(2,'0');
        return 'avc1.' + h(a[i+1]) + h(a[i+2]) + h(a[i+3]);
      }
      i += len;
    }
    return null;
  };
  const b64ToBytes = (s) => Uint8Array.from(atob(s), (c) => c.charCodeAt(0));
  const key = b64ToBytes('` + keyB64 + `');
  const delta = b64ToBytes('` + deltaB64 + `');
  let frames = 0;
  let err = '';
  const dec = new VideoDecoder({
    output: (f) => { frames++; f.close(); },
    error: (e) => { err = String(e); },
  });
  const codec = codecFromAVCC(key);
  dec.configure({ codec, optimizeForLatency: true });
  dec.decode(new EncodedVideoChunk({ type: 'key', timestamp: 0, data: avccToAnnexB(key) }));
  dec.decode(new EncodedVideoChunk({ type: 'delta', timestamp: 16666, data: avccToAnnexB(delta) }));
  await dec.flush();
  dec.close();
  globalThis.__wcFixture = JSON.stringify({ frames, err, codec, keyLen: key.length });
  } catch (e) {
    globalThis.__wcFixture = JSON.stringify({ frames: 0, err: String(e), codec: '' });
  }
})()`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<!doctype html><html><body></body></html>"))
	}))
	defer srv.Close()
	if _, err := chromedp.Run(ctx, chromedp.Navigate(srv.URL)); err != nil {
		t.Fatal(err)
	}
	if _, err := chromedp.Run(ctx, chromedp.Evaluate[struct{}](kickoff)); err != nil {
		t.Fatal(err)
	}
	var probe struct {
		Frames float64 `json:"frames"`
		Err    string  `json:"err"`
		Codec  string  `json:"codec"`
		KeyLen float64 `json:"keyLen"`
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := chromedp.Run(ctx, chromedp.Evaluate[string](`globalThis.__wcFixture || ''`))
		if err != nil || raw == "" || raw == "pending" {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		if err := json.Unmarshal([]byte(raw), &probe); err != nil {
			t.Fatalf("parse %q: %v", raw, err)
		}
		break
	}
	if probe.Codec == "" && probe.Err == "" {
		t.Fatal("timed out waiting for WebCodecs fixture decode")
	}
	if probe.Err != "" {
		t.Fatalf("WebCodecs error: %s (codec=%s keyLen=%v)", probe.Err, probe.Codec, probe.KeyLen)
	}
	if int(probe.Frames) < 2 {
		t.Fatalf("expected >=2 decoded frames, got %v (codec=%s)", probe.Frames, probe.Codec)
	}
}
