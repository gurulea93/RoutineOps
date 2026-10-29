package selfupdate

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path"
	"runtime"
	"strconv"
	"testing"
)

// TestTenGenerationSelfUpdate гоняет полный конвейер самообновления через 10
// поколений подряд без остановок: реальный TLS-сервер раздаёт манифесты и бинари,
// агент на каждом шаге делает настоящие httpCheck → httpDownload → verify
// (sha256 + ed25519) → применение → «перезапуск новой версией». Сервер по
// query-параметру ?current выдаёт следующее поколение, на вершине — ту же версию
// (обновляться больше некуда). Так проверяется, что цепочка обновлений не ломается
// и не зависает на длинной серии (Этап 7-8, нагрузочный пункт тест-плана).
func TestTenGenerationSelfUpdate(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	const gens = 10

	// Бинарь каждого поколения — уникальный контент (его и проверяем на диске).
	bins := make(map[string][]byte, gens)
	for g := 1; g <= gens; g++ {
		bins[verOf(g)] = []byte("AGENT-BINARY-GENERATION-" + strconv.Itoa(g))
	}

	// nextOf: какая версия предлагается агенту с текущей cur. Пустая cur или
	// "v0.0.0" → первое поколение; на вершине возвращаем cur (нечего обновлять).
	nextOf := func(cur string) string {
		g := genOf(cur)
		if g >= gens {
			return verOf(gens)
		}
		return verOf(g + 1)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/agent/version", func(w http.ResponseWriter, r *http.Request) {
		cur := r.URL.Query().Get("current")
		v := nextOf(cur)
		bin := bins[v]
		sum := sha256.Sum256(bin)
		m := Manifest{
			Version:   v,
			URL:       "https://" + r.Host + "/downloads/" + v,
			SHA256:    hex.EncodeToString(sum[:]),
			Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(priv, sum[:])), // legacy-поле, httpCheck требует непустым
		}
		m.ManifestSignature = base64.StdEncoding.EncodeToString(
			ed25519.Sign(priv, signedMessage(&m, runtime.GOOS, runtime.GOARCH)))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(m)
	})
	mux.HandleFunc("/downloads/", func(w http.ResponseWriter, r *http.Request) {
		v := path.Base(r.URL.Path)
		bin, ok := bins[v]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(bin)
	})

	srv := httptest.NewTLSServer(mux)
	defer srv.Close()
	client := srv.Client()
	checkURL := srv.URL + "/api/v1/agent/version"

	var (
		applied  [][]byte
		restarts int
	)
	u := &Updater{current: "v0.0.0", pubKey: pub, log: discardLog()}
	u.check = func(ctx context.Context) (*Manifest, error) { return httpCheck(ctx, client, checkURL, u.current) }
	u.download = func(ctx context.Context, url string) ([]byte, error) { return httpDownload(ctx, client, url) }
	u.replace = func(data []byte) error {
		applied = append(applied, append([]byte(nil), data...))
		return nil
	}
	u.restart = func() { restarts++ }

	ctx := context.Background()
	for g := 1; g <= gens; g++ {
		if err := u.checkAndApply(ctx); err != nil {
			t.Fatalf("поколение %d: checkAndApply: %v", g, err)
		}
		want := bins[verOf(g)]
		if len(applied) != g {
			t.Fatalf("поколение %d: применений %d, ждали %d", g, len(applied), g)
		}
		if string(applied[g-1]) != string(want) {
			t.Fatalf("поколение %d: применён не тот бинарь: got %q want %q", g, applied[g-1], want)
		}
		// Агент перезапустился новой версией — следующий цикл идёт уже от неё.
		u.current = verOf(g)
	}

	if restarts != gens {
		t.Fatalf("рестартов %d, ждали %d (по одному на поколение)", restarts, gens)
	}

	// На вершине обновляться больше некуда: ещё одна проверка не должна ничего
	// применять и не должна зацикливаться.
	before := len(applied)
	if err := u.checkAndApply(ctx); err != nil {
		t.Fatalf("проверка на вершине версии: %v", err)
	}
	if len(applied) != before {
		t.Fatalf("на последней версии агент всё равно обновился: applied %d → %d", before, len(applied))
	}
}

func verOf(g int) string { return fmt.Sprintf("v0.%d.0", g) }

// genOf вытаскивает номер поколения из "v0.N.0"; для "v0.0.0"/мусора → 0.
func genOf(v string) int {
	var maj, gen, patch int
	if _, err := fmt.Sscanf(v, "v%d.%d.%d", &maj, &gen, &patch); err != nil {
		return 0
	}
	return gen
}
