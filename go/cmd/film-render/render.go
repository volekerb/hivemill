package main

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

//go:embed card.html
var placeholderCard []byte

var filmReadyTimeout = 30 * time.Second

type session struct {
	c  *cdpClient
	id string
}

func (s *session) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	return s.c.call(ctx, s.id, method, params)
}

// eval runs a JS expression in the page, awaiting a returned promise, and returns the JSON value.
func (s *session) eval(ctx context.Context, expr string) (json.RawMessage, error) {
	raw, err := s.call(ctx, "Runtime.evaluate", map[string]any{"expression": expr, "awaitPromise": true, "returnByValue": true})
	if err != nil {
		return nil, err
	}
	var r struct {
		Result  struct{ Value json.RawMessage } `json:"result"`
		Details *struct {
			Text      string `json:"text"`
			Exception struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("bad Runtime.evaluate reply: %w", err)
	}
	if r.Details != nil {
		return nil, fmt.Errorf("page script error: %s %s", r.Details.Text, r.Details.Exception.Description)
	}
	return r.Result.Value, nil
}

func openSession(ctx context.Context, c *cdpClient, w, h int) (*session, error) {
	raw, err := c.call(ctx, "", "Target.createTarget", map[string]any{"url": "about:blank"})
	if err != nil {
		return nil, err
	}
	var t struct {
		TargetID string `json:"targetId"`
	}
	if err := json.Unmarshal(raw, &t); err != nil || t.TargetID == "" {
		return nil, fmt.Errorf("Target.createTarget returned no targetId")
	}
	raw, err = c.call(ctx, "", "Target.attachToTarget", map[string]any{"targetId": t.TargetID, "flatten": true})
	if err != nil {
		return nil, err
	}
	var a struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(raw, &a); err != nil || a.SessionID == "" {
		return nil, fmt.Errorf("Target.attachToTarget returned no sessionId")
	}
	s := &session{c: c, id: a.SessionID}
	if _, err := s.call(ctx, "Page.enable", nil); err != nil {
		return nil, err
	}
	_, err = s.call(ctx, "Emulation.setDeviceMetricsOverride", map[string]any{"width": w, "height": h, "deviceScaleFactor": 1, "mobile": false})
	return s, err
}

func (s *session) loadScene(ctx context.Context, url string) error {
	raw, err := s.call(ctx, "Page.navigate", map[string]any{"url": url})
	if err != nil {
		return err
	}
	var n struct {
		ErrorText string `json:"errorText"`
	}
	if json.Unmarshal(raw, &n) == nil && n.ErrorText != "" {
		return fmt.Errorf("navigation to %s failed: %s", url, n.ErrorText)
	}
	deadline := time.Now().Add(filmReadyTimeout)
	for {
		v, err := s.eval(ctx, "window.filmReady === true")
		if err == nil && string(v) == "true" {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("window.filmReady was not true within %s for %s", filmReadyTimeout, url)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

const captionJS = `(async () => {
  const text = %s, font = %s;
  if (font) {
    const st = document.createElement('style');
    st.textContent = '@font-face{font-family:"Inter";font-weight:700;src:url("' + font + '") format("woff2")}';
    document.head.appendChild(st);
  }
  const old = document.getElementById('film-caption'); if (old) old.remove();
  const d = document.createElement('div');
  d.id = 'film-caption';
  d.textContent = text;
  d.style.cssText = 'position:fixed;left:0;right:0;bottom:0;height:140px;box-sizing:border-box;padding:0 96px;display:flex;align-items:center;justify-content:center;text-align:center;background:rgba(10,12,18,0.72);color:#fff;font:700 44px/1.2 Inter,sans-serif;z-index:2147483647';
  document.body.appendChild(d);
  try { await document.fonts.load('700 44px Inter', text); } catch (e) {}
  return true;
})()`

func (s *session) injectCaption(ctx context.Context, caption, fontURL string) error {
	t, _ := json.Marshal(caption)
	f, _ := json.Marshal(fontURL)
	_, err := s.eval(ctx, fmt.Sprintf(captionJS, t, f))
	return err
}

func (s *session) frame(ctx context.Context, t float64) ([]byte, error) {
	if _, err := s.eval(ctx, "window.seek("+strconv.FormatFloat(t, 'f', -1, 64)+")"); err != nil {
		return nil, fmt.Errorf("seek(%g): %w", t, err)
	}
	raw, err := s.call(ctx, "Page.captureScreenshot", map[string]any{"format": "png"})
	if err != nil {
		return nil, err
	}
	var r struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("bad captureScreenshot reply: %w", err)
	}
	return base64.StdEncoding.DecodeString(r.Data)
}

// encoder is the ffmpeg process fed one continuous PNG stream.
type encoder struct {
	p      *proc
	stdin  io.WriteCloser
	stderr *bytes.Buffer
}

func startEncoder(ffmpeg string, fps float64, out string) (*encoder, error) {
	cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-y", "-f", "image2pipe",
		"-framerate", strconv.FormatFloat(fps, 'f', -1, 64), "-i", "-", "-c:v", "libx264", "-pix_fmt", "yuv420p",
		"-movflags", "+faststart", "-an", out)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, failf("ffmpeg stdin: %v", err)
	}
	var buf bytes.Buffer
	cmd.Stderr = &buf
	p, err := startProc("ffmpeg", cmd)
	if err != nil {
		return nil, err
	}
	return &encoder{p: p, stdin: stdin, stderr: &buf}, nil
}

func (e *encoder) finish() error {
	e.stdin.Close()
	<-e.p.done
	if e.p.err != nil {
		return failf("ffmpeg failed: %v: %s", e.p.err, strings.TrimSpace(e.stderr.String()))
	}
	return nil
}

func probeDuration(ffprobe, out string) (float64, error) {
	b, err := exec.Command(ffprobe, "-v", "error", "-show_entries", "format=duration", "-of", "default=nw=1:nk=1", out).Output()
	if err != nil {
		return 0, failf("ffprobe failed on %s: %v", out, err)
	}
	d, err := strconv.ParseFloat(strings.TrimSpace(string(b)), 64)
	if err != nil {
		return 0, failf("ffprobe printed %q, not a duration", strings.TrimSpace(string(b)))
	}
	return d, nil
}

func frameCount(sc scene, fps float64) int { return int(math.Round((sc.End - sc.Start) * fps)) }

// renderFilm drives the browser through every scene and streams the frames to ffmpeg.
func renderFilm(ctx context.Context, s *session, enc *encoder, sb *storyboard, o *options, work string, stdout, stderr io.Writer) error {
	fontPath := filepath.Join(o.repo, "docs", "demo", "fonts", "Inter-Bold.woff2")
	fontURL := fileURL(fontPath)
	if _, err := os.Stat(fontPath); err != nil {
		fmt.Fprintf(stderr, "film-render: warning: %s not found; captions fall back to a system sans font\n", fontPath)
		fontURL = ""
	}
	cardPath := filepath.Join(work, "placeholder.html")
	if err := os.WriteFile(cardPath, placeholderCard, 0o644); err != nil {
		return failf("cannot write %s: %v", cardPath, err)
	}
	for _, sc := range sb.Scenes {
		page := filepath.Join(o.scenes, sc.ID+".html")
		if sc.Placeholder {
			page = cardPath
		}
		abs, err := filepath.Abs(page)
		if err != nil {
			return failf("%v", err)
		}
		if err := s.loadScene(ctx, fileURL(abs)); err != nil {
			return failf("scene %q: %v", sc.ID, err)
		}
		if sc.Caption != "" {
			if err := s.injectCaption(ctx, sc.Caption, fontURL); err != nil {
				return failf("scene %q: caption: %v", sc.ID, err)
			}
		}
		n := frameCount(sc, sb.FPS)
		began := time.Now()
		for k := 0; k < n; k++ {
			png, err := s.frame(ctx, float64(k)/sb.FPS)
			if err != nil {
				return failf("scene %q frame %d: %v", sc.ID, k, err)
			}
			if _, err := enc.stdin.Write(png); err != nil {
				return failf("scene %q frame %d: ffmpeg stopped accepting frames: %v: %s", sc.ID, k, err, strings.TrimSpace(enc.stderr.String()))
			}
		}
		el := time.Since(began).Seconds()
		fmt.Fprintf(stdout, "scene %s: %d frames, %.1f frames/s\n", sc.ID, n, float64(n)/math.Max(el, 1e-9))
	}
	return nil
}
