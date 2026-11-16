package httpapi_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/martoast/papaya/internal/httpapi"
	"github.com/martoast/papaya/internal/hub"
	"github.com/martoast/papaya/internal/sshx"
	"github.com/martoast/papaya/internal/transcribe"
	"github.com/martoast/papaya/internal/tsnode"
	"github.com/martoast/papaya/test/harness"
)

// Voice turned on from the app: the pasted key is checked with the API, saved
// 0600, switches transcription on at once, and is never handed back.
func TestVoiceKeyFromTheApp(t *testing.T) {
	const good = "sk-test-0123456789abcdefghij"
	openai := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" && r.Header.Get("Authorization") == "Bearer "+good {
			w.Write([]byte(`{"data":[]}`))
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"bad key","code":"invalid_api_key"}}`))
	}))
	t.Cleanup(openai.Close)

	dir := t.TempDir()
	keyPath := filepath.Join(dir, "state", "openai-key")
	tr := transcribe.New(transcribe.Options{BaseURL: openai.URL})
	h := hub.New(sshx.NewPool(harness.NewCountingDialer(nil), sshx.Config{}), hub.Options{UploadDir: filepath.Join(dir, "up")})
	t.Cleanup(func() { h.CloseAll("test over") })
	api := httpapi.NewWithTranscriber(h,
		tsnode.NewAuth(whoIs{"alice@example.com"}, map[string]bool{"alice@example.com": true}, nil),
		nil, nil, tr, httpapi.Options{VoiceKeyPath: keyPath})
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	caps := func() httpapi.Capabilities {
		res, err := http.Get(srv.URL + "/api/capabilities")
		require.NoError(t, err)
		defer res.Body.Close()
		var c httpapi.Capabilities
		require.NoError(t, json.NewDecoder(res.Body).Decode(&c))
		return c
	}
	put := func(key string) int {
		b, _ := json.Marshal(map[string]string{"key": key})
		req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/voice/key", bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		res.Body.Close()
		return res.StatusCode
	}

	c := caps()
	require.False(t, c.Transcription)
	require.True(t, c.VoiceSettable)

	require.Equal(t, 400, put("short"))
	require.Equal(t, 400, put("sk-wrong-0123456789abcdefghij"), "a key the API rejects is not saved")
	_, err := os.Stat(keyPath)
	require.True(t, os.IsNotExist(err))

	require.Equal(t, 200, put(good))
	require.True(t, caps().Transcription, "on at once, no restart")
	st, err := os.Stat(keyPath)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), st.Mode().Perm())
	require.Equal(t, good, httpapi.LoadVoiceKey(keyPath))

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/voice/key", nil)
	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	res.Body.Close()
	require.Equal(t, 200, res.StatusCode)
	require.False(t, caps().Transcription)
	_, err = os.Stat(keyPath)
	require.True(t, os.IsNotExist(err))
}
