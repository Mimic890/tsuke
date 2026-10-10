package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"
	"github.com/makiuchi-d/gozxing"
	"github.com/makiuchi-d/gozxing/qrcode"

	"github.com/Mimic890/tsuke/internal/config"
	"github.com/Mimic890/tsuke/internal/firefly"
	"github.com/Mimic890/tsuke/internal/store"
)

const (
	ownerID  = 42
	chatID   = 42
	tgToken  = "111:TESTTOKEN"
	fakeBody = `{"data":{"type":"transactions","id":"77","attributes":{"transactions":[{"transaction_journal_id":"88","type":"withdrawal","date":"2026-10-10T15:20:00+03:00","amount":"350.000000000000","currency_symbol":"₽","description":"кофе","source_id":"1","source_name":"Т-Банк","destination_id":"9","destination_name":"(no name)","category_name":"Еда","tags":[],"notes":null}]}}}`
)

// fakeTG records Bot API calls.
type fakeTG struct {
	t     *testing.T
	mu    sync.Mutex
	calls []call
	next  int
	file  []byte
}

type call struct {
	method string
	body   map[string]any
}

func (f *fakeTG) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/file/") {
		_, _ = w.Write(f.file)
		return
	}
	method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	body := map[string]any{}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		// The client sends fields as multipart form values; nested ones are JSON strings.
		_ = r.ParseMultipartForm(32 << 20)
		for k, v := range r.MultipartForm.Value {
			var parsed any
			if err := json.Unmarshal([]byte(v[0]), &parsed); err == nil {
				body[k] = parsed
			} else {
				body[k] = v[0]
			}
		}
	} else {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
	}
	f.mu.Lock()
	f.calls = append(f.calls, call{method, body})
	f.next++
	id := 1000 + f.next
	f.mu.Unlock()

	var result any = true
	switch method {
	case "sendMessage", "editMessageText":
		mid := id
		if v, ok := body["message_id"].(float64); ok {
			mid = int(v)
		}
		result = map[string]any{"message_id": mid, "date": 0, "chat": map[string]any{"id": chatID, "type": "private"}}
	case "getFile":
		result = map[string]any{"file_id": body["file_id"], "file_path": "photos/a.png", "file_size": len(f.file)}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
}

func (f *fakeTG) reset() {
	f.mu.Lock()
	f.calls = nil
	f.mu.Unlock()
}

func (f *fakeTG) find(method string) []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []call
	for _, c := range f.calls {
		if c.method == method {
			out = append(out, c)
		}
	}
	return out
}

// last returns the text of the most recent sendMessage/editMessageText.
func (f *fakeTG) last() (text string, c call) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.calls) - 1; i >= 0; i-- {
		if m := f.calls[i].method; m == "sendMessage" || m == "editMessageText" {
			s, _ := f.calls[i].body["text"].(string)
			return s, f.calls[i]
		}
	}
	return "", call{}
}

// buttons returns text -> callback_data of the call's inline keyboard.
func buttons(c call) map[string]string {
	out := map[string]string{}
	rm, _ := c.body["reply_markup"].(map[string]any)
	rows, _ := rm["inline_keyboard"].([]any)
	for _, r := range rows {
		for _, b := range r.([]any) {
			m := b.(map[string]any)
			out[m["text"].(string)], _ = m["callback_data"].(string)
		}
	}
	return out
}

// fakeFF is a minimal Firefly III.
type fakeFF struct {
	mu      sync.Mutex
	created []map[string]any
	updated []map[string]any
	deleted []string
	attach  []string
	upload  [][]byte
}

func (f *fakeFF) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	list := func(items string) {
		_, _ = w.Write([]byte(`{"data":[` + items + `],"meta":{"pagination":{"total_pages":1}}}`))
	}
	acc := func(id, name string) string {
		return `{"id":"` + id + `","attributes":{"name":"` + name + `","active":true,"current_balance":"1500.50","currency_symbol":"₽"}}`
	}
	switch {
	case r.URL.Path == "/api/v1/accounts":
		switch r.URL.Query().Get("type") {
		case "asset":
			list(acc("1", "Т-Банк") + "," + acc("2", "Наличные"))
		case "expense":
			list(acc("9", "Кофейня"))
		default:
			list(acc("10", "Работодатель"))
		}
	case r.URL.Path == "/api/v1/categories":
		list(`{"id":"1","attributes":{"name":"Еда"}},{"id":"2","attributes":{"name":"Транспорт"}}`)
	case r.URL.Path == "/api/v1/budgets":
		list(`{"id":"3","attributes":{"name":"Месяц"}}`)
	case r.URL.Path == "/api/v1/tags":
		list(`{"id":"4","attributes":{"tag":"работа"}}`)
	case r.URL.Path == "/api/v1/search/transactions":
		list("")
	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/transactions":
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.created = append(f.created, body)
		f.mu.Unlock()
		_, _ = w.Write([]byte(fakeBody))
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/transactions/77":
		_, _ = w.Write([]byte(fakeBody))
	case r.Method == http.MethodPut && r.URL.Path == "/api/v1/transactions/77":
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.updated = append(f.updated, body)
		f.mu.Unlock()
		_, _ = w.Write([]byte(fakeBody))
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/transactions":
		list(strings.TrimSuffix(strings.TrimPrefix(fakeBody, `{"data":`), `}`))
	case r.Method == http.MethodDelete:
		f.mu.Lock()
		f.deleted = append(f.deleted, r.URL.Path)
		f.mu.Unlock()
		w.WriteHeader(204)
	case r.URL.Path == "/api/v1/attachments":
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.attach = append(f.attach, body["attachable_id"]+"/"+body["filename"])
		f.mu.Unlock()
		_, _ = w.Write([]byte(`{"data":{"id":"5"}}`))
	case strings.HasSuffix(r.URL.Path, "/upload"):
		data, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.upload = append(f.upload, data)
		f.mu.Unlock()
		w.WriteHeader(204)
	default:
		http.NotFound(w, r)
	}
}

type env struct {
	ffURL string
	b     *Bot
	tg    *fakeTG
	ff    *fakeFF
}

func newEnv(t *testing.T) *env {
	t.Helper()
	tg := &fakeTG{t: t}
	tgSrv := httptest.NewServer(tg)
	t.Cleanup(tgSrv.Close)
	ff := &fakeFF{}
	ffSrv := httptest.NewServer(ff)
	t.Cleanup(ffSrv.Close)

	loc := time.FixedZone("MSK", 3*3600)
	dir := t.TempDir()
	cfg := &config.Config{
		TelegramToken: tgToken, FireflyURL: ffSrv.URL, FireflyPublic: "https://ff.example.com", FireflyToken: "fft",
		AllowedUserIDs: map[int64]struct{}{ownerID: {}}, Location: loc, DataDir: dir, CacheRefresh: time.Hour,
		DefaultSource: "т-банк",
	}
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	log := slog.New(slog.NewTextHandler(testLogOut(), &slog.HandlerOptions{Level: slog.LevelDebug}))
	b, err := New(Options{Config: cfg, Firefly: firefly.New(ffSrv.URL, "fft", log), Store: st, Logger: log, TelegramURL: tgSrv.URL})
	if err != nil {
		t.Fatal(err)
	}
	b.now = func() time.Time { return time.Date(2026, 10, 10, 15, 20, 0, 0, loc) }
	st.SetClock(b.now)
	if err := b.RefreshRefs(context.Background()); err != nil {
		t.Fatal(err)
	}
	return &env{b: b, tg: tg, ff: ff, ffURL: ffSrv.URL}
}

var msgSeq = 100

func (e *env) say(text string) {
	msgSeq++
	e.b.Handle(context.Background(), &models.Update{Message: &models.Message{
		ID: msgSeq, From: &models.User{ID: ownerID}, Chat: models.Chat{ID: chatID, Type: models.ChatTypePrivate}, Text: text,
	}})
}

func (e *env) press(msgID int, data string) {
	e.b.Handle(context.Background(), &models.Update{CallbackQuery: &models.CallbackQuery{
		ID: "cb", From: models.User{ID: ownerID}, Data: data,
		Message: models.MaybeInaccessibleMessage{Type: models.MaybeInaccessibleMessageTypeMessage, Message: &models.Message{
			ID: msgID, Chat: models.Chat{ID: chatID, Type: models.ChatTypePrivate},
		}},
	}})
}

// messageID returns the message_id from the most recent card call.
func (e *env) cardID() int {
	e.tg.mu.Lock()
	defer e.tg.mu.Unlock()
	for i := len(e.tg.calls) - 1; i >= 0; i-- {
		c := e.tg.calls[i]
		switch c.method {
		case "editMessageText":
			return int(c.body["message_id"].(float64))
		case "sendMessage":
			return 1000 + i + 1 // matches fakeTG numbering (one id per call)
		}
	}
	return 0
}

func (e *env) pressBtn(t *testing.T, label string) {
	t.Helper()
	_, c := e.tg.last()
	data, ok := buttons(c)[label]
	if !ok {
		t.Fatalf("no button %q in %v", label, buttons(c))
	}
	e.press(e.cardID(), data)
}

func TestStrangerGetsNoReply(t *testing.T) {
	e := newEnv(t)
	e.tg.reset()
	e.b.Handle(context.Background(), &models.Update{Message: &models.Message{
		ID: 1, From: &models.User{ID: 999}, Chat: models.Chat{ID: 999, Type: models.ChatTypePrivate}, Text: "350 кофе",
	}})
	e.b.Handle(context.Background(), &models.Update{CallbackQuery: &models.CallbackQuery{ID: "x", From: models.User{ID: 999}, Data: "d:abc:sv"}})
	e.b.Handle(context.Background(), &models.Update{Message: &models.Message{
		ID: 2, From: &models.User{ID: ownerID}, Chat: models.Chat{ID: -5, Type: models.ChatTypeGroup}, Text: "350 кофе",
	}})
	if n := len(e.tg.calls); n != 0 {
		t.Fatalf("expected no Telegram calls, got %d: %+v", n, e.tg.calls)
	}
}

func TestQuickInputCardAndSave(t *testing.T) {
	e := newEnv(t)
	e.tg.reset()
	e.say("350 кофе #еда")

	text, c := e.tg.last()
	for _, want := range []string{"Расход", "350", "Т-Банк", "Еда", "кофе", "10.10.2026 15:20"} {
		if !strings.Contains(text, want) {
			t.Errorf("card lacks %q:\n%s", want, text)
		}
	}
	btns := buttons(c)
	for _, want := range []string{"✅ Сохранить", "✖ Отмена", "Сумма", "Категория", "Дата", "Время"} {
		if _, ok := btns[want]; !ok {
			t.Errorf("missing button %q", want)
		}
	}
	for label, data := range btns {
		if len(data) > 64 {
			t.Errorf("callback data of %q too long: %d", label, len(data))
		}
	}

	saveData := btns["✅ Сохранить"]
	e.pressBtn(t, "✅ Сохранить")
	if len(e.ff.created) != 1 {
		t.Fatalf("created = %d", len(e.ff.created))
	}
	body := e.ff.created[0]
	if body["apply_rules"] != true || body["fire_webhooks"] != true || body["error_if_duplicate_hash"] != true {
		t.Errorf("flags = %v", body)
	}
	sp := body["transactions"].([]any)[0].(map[string]any)
	for k, v := range map[string]any{
		"type": "withdrawal", "amount": "350.00", "source_id": "1", "description": "кофе",
		"category_name": "Еда", "date": "2026-10-10T15:20:00+03:00",
	} {
		if sp[k] != v {
			t.Errorf("%s = %v, want %v", k, sp[k], v)
		}
	}
	if ext, _ := sp["external_id"].(string); !strings.HasPrefix(ext, "tsuke-") {
		t.Errorf("external_id = %v", sp["external_id"])
	}

	text, c = e.tg.last()
	if !strings.Contains(text, "Сохранено") || !strings.Contains(text, "https://ff.example.com/transactions/show/77") {
		t.Errorf("saved card:\n%s", text)
	}
	if _, ok := buttons(c)["✏️ Изменить"]; !ok {
		t.Errorf("saved card buttons: %v", buttons(c))
	}

	// A second press on the old Save button must not create anything.
	e.press(e.cardID(), saveData)
	if len(e.ff.created) != 1 {
		t.Errorf("duplicate creation: %d", len(e.ff.created))
	}

	// Usage counters affect ordering.
	items := e.b.items(context.Background(), kCategory)
	if items[0].Name != "Еда" {
		t.Errorf("most used first: %v", items)
	}
}

func TestNoAmountHint(t *testing.T) {
	e := newEnv(t)
	e.tg.reset()
	e.say("просто текст")
	text, _ := e.tg.last()
	if !strings.Contains(text, "Не нашёл сумму") {
		t.Errorf("text = %q", text)
	}
}

func TestDepositAndTransferInput(t *testing.T) {
	e := newEnv(t)
	e.say("+50000 зарплата")
	text, _ := e.tg.last()
	if !strings.Contains(text, "Доход") || !strings.Contains(text, "50\u00a0000") {
		t.Errorf("deposit card:\n%s", text)
	}
	e.say("5000 тбанк > наличные")
	text, _ = e.tg.last()
	if !strings.Contains(text, "Перевод") || !strings.Contains(text, "Т-Банк") || !strings.Contains(text, "Наличные") {
		t.Errorf("transfer card:\n%s", text)
	}
	e.say("100 такси @нет_такого")
	text, _ = e.tg.last()
	if !strings.Contains(text, "не найден") {
		t.Errorf("unknown account must be reported:\n%s", text)
	}
}

func TestEditAmountByTyping(t *testing.T) {
	e := newEnv(t)
	e.say("350 кофе")
	e.pressBtn(t, "Сумма")
	text, _ := e.tg.last()
	if !strings.Contains(text, "Введите сумму") {
		t.Fatalf("prompt:\n%s", text)
	}
	e.say("1 200,50")
	text, c := e.tg.last()
	if !strings.Contains(text, "1\u00a0200,50") {
		t.Errorf("card after typing:\n%s", text)
	}
	if len(e.tg.find("deleteMessage")) == 0 {
		t.Error("typed input message should be removed")
	}
	if _, ok := buttons(c)["✅ Сохранить"]; !ok {
		t.Error("should be back on the card")
	}
	e.say("мусор")
	// New quick input must still work after the prompt closed.
	e.say("99 хлеб")
	text, _ = e.tg.last()
	if !strings.Contains(text, "99") {
		t.Errorf("new draft expected:\n%s", text)
	}
}

func TestPickerSearchAndSelect(t *testing.T) {
	e := newEnv(t)
	e.say("350 кофе")
	e.pressBtn(t, "Категория")
	text, c := e.tg.last()
	if !strings.Contains(text, "Категория") {
		t.Fatalf("picker:\n%s", text)
	}
	if _, ok := buttons(c)["Еда"]; !ok {
		t.Fatalf("buttons: %v", buttons(c))
	}
	e.say("транс")
	_, c = e.tg.last()
	b := buttons(c)
	if _, ok := b["Транспорт"]; !ok {
		t.Fatalf("search result: %v", b)
	}
	if _, ok := b["Еда"]; ok {
		t.Errorf("filter should hide Еда: %v", b)
	}
	e.pressBtn(t, "Транспорт")
	text, _ = e.tg.last()
	if !strings.Contains(text, "Транспорт") || !strings.Contains(text, "✅") && !strings.Contains(text, "Новая операция") {
		t.Errorf("card after pick:\n%s", text)
	}

	// New free-text category through the "➕" button.
	e.pressBtn(t, "Категория")
	e.say("Подарки")
	_, c = e.tg.last()
	var plus string
	for label := range buttons(c) {
		if strings.HasPrefix(label, "➕") {
			plus = label
		}
	}
	if plus == "" {
		t.Fatalf("expected ➕ button: %v", buttons(c))
	}
	e.pressBtn(t, plus)
	text, _ = e.tg.last()
	if !strings.Contains(text, "Подарки") {
		t.Errorf("new category missing:\n%s", text)
	}
}

func TestDateAndTimePickers(t *testing.T) {
	e := newEnv(t)
	e.say("350 кофе")
	e.pressBtn(t, "Дата")
	text, c := e.tg.last()
	if !strings.Contains(text, "10.10.2026") {
		t.Fatalf("date screen:\n%s", text)
	}
	b := buttons(c)
	for _, want := range []string{"Сейчас", "Вчера", "2 дня назад", "Октябрь 2026", "[10]"} {
		if _, ok := b[want]; !ok {
			t.Errorf("missing %q in %v", want, b)
		}
	}
	e.pressBtn(t, "Вчера")
	text, _ = e.tg.last()
	if !strings.Contains(text, "09.10.2026 15:20") {
		t.Errorf("yesterday:\n%s", text)
	}

	e.pressBtn(t, "Дата")
	e.pressBtn(t, "‹")
	_, c = e.tg.last()
	if _, ok := buttons(c)["Сентябрь 2026"]; !ok {
		t.Errorf("prev month: %v", buttons(c))
	}
	e.pressBtn(t, "15") // 15 September
	text, _ = e.tg.last()
	if !strings.Contains(text, "15.09.2026 15:20") {
		t.Errorf("calendar pick:\n%s", text)
	}

	e.pressBtn(t, "Время")
	e.pressBtn(t, "Вечер")
	text, _ = e.tg.last()
	if !strings.Contains(text, "19:00") {
		t.Errorf("preset:\n%s", text)
	}
	e.pressBtn(t, "+15 мин")
	e.pressBtn(t, "+1 ч")
	text, _ = e.tg.last()
	if !strings.Contains(text, "20:15") {
		t.Errorf("stepper:\n%s", text)
	}
	e.say("вчера 9:15")
	text, _ = e.tg.last()
	if !strings.Contains(text, "09.10.2026 09:15") {
		t.Errorf("typed when:\n%s", text)
	}

	e.pressBtn(t, "✅ Сохранить")
	sp := e.ff.created[0]["transactions"].([]any)[0].(map[string]any)
	if sp["date"] != "2026-10-09T09:15:00+03:00" {
		t.Errorf("date sent = %v", sp["date"])
	}
}

func TestFireflyValidationErrorKeepsDraft(t *testing.T) {
	e := newEnv(t)
	e.say("350 кофе")
	// Make Firefly reject the next create.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/transactions" {
			w.WriteHeader(422)
			_, _ = w.Write([]byte(`{"message":"The given data was invalid.","errors":{"transactions.0.category_name":["Category is bad."]}}`))
			return
		}
		e.ff.ServeHTTP(w, r)
	}))
	defer srv.Close()
	e.b.ff = firefly.New(srv.URL, "fft", e.b.log)
	e.pressBtn(t, "✅ Сохранить")
	text, c := e.tg.last()
	if !strings.Contains(text, "Category is bad.") || !strings.Contains(text, "350") {
		t.Errorf("error card:\n%s", text)
	}
	if _, ok := buttons(c)["✅ Сохранить"]; !ok {
		t.Error("draft must remain editable")
	}
	// Once Firefly accepts it, the same draft saves fine.
	e.b.ff = firefly.New(e.ffURL, "fft", e.b.log)
	e.pressBtn(t, "✅ Сохранить")
	if len(e.ff.created) != 1 {
		t.Errorf("retry should create once, got %d", len(e.ff.created))
	}
}

func TestSavedEditAndDelete(t *testing.T) {
	e := newEnv(t)
	e.say("350 кофе")
	e.pressBtn(t, "✅ Сохранить")
	e.pressBtn(t, "🗑 Удалить")
	text, _ := e.tg.last()
	if !strings.Contains(text, "Удалить эту транзакцию") {
		t.Fatalf("confirm:\n%s", text)
	}
	e.pressBtn(t, "Нет")
	text, _ = e.tg.last()
	if !strings.Contains(text, "Сохранено") {
		t.Errorf("back to saved card:\n%s", text)
	}
	e.pressBtn(t, "✏️ Изменить")
	text, _ = e.tg.last()
	if !strings.Contains(text, "Изменение") {
		t.Errorf("edit card:\n%s", text)
	}
	e.pressBtn(t, "✅ Сохранить")
	if len(e.ff.created) != 1 || len(e.ff.updated) != 1 {
		t.Errorf("editing must update, not create: created=%d updated=%d", len(e.ff.created), len(e.ff.updated))
	}
	if sp := e.ff.updated[0]["transactions"].([]any)[0].(map[string]any); sp["transaction_journal_id"] != "88" {
		t.Errorf("update payload = %v", sp)
	}
	e.pressBtn(t, "🗑 Удалить")
	e.pressBtn(t, "Да, удалить")
	if len(e.ff.deleted) != 1 || e.ff.deleted[0] != "/api/v1/transactions/77" {
		t.Errorf("deleted = %v", e.ff.deleted)
	}
}

func TestUndo(t *testing.T) {
	e := newEnv(t)
	e.say("/undo")
	if text, _ := e.tg.last(); !strings.Contains(text, "ещё ничего не создавал") {
		t.Errorf("empty undo: %q", text)
	}
	e.say("350 кофе")
	e.pressBtn(t, "✅ Сохранить")
	e.say("/undo")
	text, _ := e.tg.last()
	if !strings.Contains(text, "Удалить последнюю операцию?") {
		t.Fatalf("undo confirm:\n%s", text)
	}
	e.pressBtn(t, "Да, удалить")
	if len(e.ff.deleted) != 1 {
		t.Errorf("deleted = %v", e.ff.deleted)
	}
}

func TestCommands(t *testing.T) {
	e := newEnv(t)
	for cmd, want := range map[string]string{
		"/start":        "Firefly",
		"/help":         "5000 тбанк &gt; наличные",
		"/balance":      "Т-Банк",
		"/today":        "Еда",
		"/last 3":       "кофе",
		"/refresh":      "Обновлено",
		"/nonsense":     "Неизвестная команда",
		"/cancel":       "Нет открытого черновика",
		"/help@somebot": "Быстрый ввод",
	} {
		e.tg.reset()
		e.say(cmd)
		text, _ := e.tg.last()
		if !strings.Contains(text, want) {
			t.Errorf("%s: want %q in:\n%s", cmd, want, text)
		}
	}
	e.say("350 кофе")
	e.say("/cancel")
	if text, _ := e.tg.last(); !strings.Contains(text, "Отменено") {
		t.Errorf("cancel: %q", text)
	}
}

func TestGuidedNew(t *testing.T) {
	e := newEnv(t)
	e.say("/new")
	text, _ := e.tg.last()
	if !strings.Contains(text, "Выберите тип") {
		t.Fatalf("step 1:\n%s", text)
	}
	e.pressBtn(t, "Расход")
	if text, _ = e.tg.last(); !strings.Contains(text, "Введите сумму") {
		t.Fatalf("step 2:\n%s", text)
	}
	e.say("250")
	if text, _ = e.tg.last(); !strings.Contains(text, "Откуда") {
		t.Fatalf("step 3:\n%s", text)
	}
	e.pressBtn(t, "Пропустить ›") // keep default account
	if text, _ = e.tg.last(); !strings.Contains(text, "Куда") {
		t.Fatalf("step 4:\n%s", text)
	}
	e.say("Пятёрочка")
	_, c := e.tg.last()
	for label := range buttons(c) {
		if strings.HasPrefix(label, "➕") {
			e.pressBtn(t, label)
		}
	}
	if text, _ = e.tg.last(); !strings.Contains(text, "Категория") {
		t.Fatalf("step 5:\n%s", text)
	}
	e.pressBtn(t, "Еда")
	if text, _ = e.tg.last(); !strings.Contains(text, "Напишите описание") {
		t.Fatalf("step 6:\n%s", text)
	}
	e.say("продукты")
	text, _ = e.tg.last()
	if !strings.Contains(text, "Пятёрочка") || !strings.Contains(text, "продукты") {
		t.Fatalf("final card:\n%s", text)
	}
	e.pressBtn(t, "✅ Сохранить")
	sp := e.ff.created[0]["transactions"].([]any)[0].(map[string]any)
	if sp["destination_name"] != "Пятёрочка" || sp["amount"] != "250.00" || sp["category_name"] != "Еда" {
		t.Errorf("payload = %v", sp)
	}
}

func qrImage(t *testing.T, payload string) []byte {
	t.Helper()
	m, err := qrcode.NewQRCodeWriter().EncodeWithoutHint(payload, gozxing.BarcodeFormat_QR_CODE, 300, 300)
	if err != nil {
		t.Fatal(err)
	}
	img := image.NewGray(image.Rect(0, 0, m.GetWidth(), m.GetHeight()))
	for y := 0; y < m.GetHeight(); y++ {
		for x := 0; x < m.GetWidth(); x++ {
			if !m.Get(x, y) {
				img.Pix[y*img.Stride+x] = 255
			}
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func (e *env) sendPhoto(caption string, replyTo int) {
	msgSeq++
	m := &models.Message{
		ID: msgSeq, From: &models.User{ID: ownerID}, Chat: models.Chat{ID: chatID, Type: models.ChatTypePrivate},
		Photo: []models.PhotoSize{{FileID: "small", FileUniqueID: "u1"}, {FileID: "big", FileUniqueID: "u2"}}, Caption: caption,
	}
	if replyTo != 0 {
		m.ReplyToMessage = &models.Message{ID: replyTo}
	}
	e.b.Handle(context.Background(), &models.Update{Message: m})
}

func TestReceiptPhotoWithQR(t *testing.T) {
	e := newEnv(t)
	e.tg.file = qrImage(t, "t=20261010T0853&s=350.00&fn=9960440300000000&i=12345&fp=1234567890&n=1")
	e.sendPhoto("", 0)
	text, _ := e.tg.last()
	for _, want := range []string{"Расход", "350", "10.10.2026 08:53", "fn=9960440300000000 i=12345 fp=1234567890", "Файлов: 1"} {
		if !strings.Contains(text, want) {
			t.Errorf("card lacks %q:\n%s", want, text)
		}
	}
	e.pressBtn(t, "✅ Сохранить")
	if len(e.ff.attach) != 1 || e.ff.attach[0] != "88/photo_u2.jpg" {
		t.Errorf("attachments = %v", e.ff.attach)
	}
	if len(e.ff.upload) != 1 || !bytes.Equal(e.ff.upload[0], e.tg.file) {
		t.Errorf("uploaded %d blobs", len(e.ff.upload))
	}
	sp := e.ff.created[0]["transactions"].([]any)[0].(map[string]any)
	if !strings.Contains(sp["notes"].(string), "fp=1234567890") {
		t.Errorf("notes = %v", sp["notes"])
	}
	if text, _ = e.tg.last(); !strings.Contains(text, "Прикреплено файлов: 1") {
		t.Errorf("saved card:\n%s", text)
	}
}

func TestReceiptReturnIsDeposit(t *testing.T) {
	e := newEnv(t)
	e.tg.file = qrImage(t, "t=20261010T0853&s=120.00&fn=1&i=2&fp=3&n=2")
	e.sendPhoto("", 0)
	text, _ := e.tg.last()
	if !strings.Contains(text, "Доход") {
		t.Errorf("n=2 must be a deposit:\n%s", text)
	}
	e.pressBtn(t, "✅ Сохранить")
	sp := e.ff.created[0]["transactions"].([]any)[0].(map[string]any)
	if sp["type"] != "deposit" || sp["destination_id"] != "1" {
		t.Errorf("payload = %v", sp)
	}
}

func TestReceiptWithoutQRAsksAmount(t *testing.T) {
	e := newEnv(t)
	e.tg.file = []byte("\x89PNG not really")
	e.sendPhoto("", 0)
	text, _ := e.tg.last()
	if !strings.Contains(text, "QR-код чека не найден") {
		t.Fatalf("prompt:\n%s", text)
	}
	e.say("777")
	text, _ = e.tg.last()
	if !strings.Contains(text, "777") || !strings.Contains(text, "Файлов: 1") {
		t.Errorf("card:\n%s", text)
	}
}

func TestPhotoReplyAttachesToSaved(t *testing.T) {
	e := newEnv(t)
	e.say("350 кофе")
	e.pressBtn(t, "✅ Сохранить")
	card := e.cardID()
	e.tg.file = []byte("not a qr")
	e.tg.reset()
	e.sendPhoto("", card)
	if len(e.ff.attach) != 1 || !strings.HasPrefix(e.ff.attach[0], "88/") {
		t.Errorf("attachments = %v", e.ff.attach)
	}
	if text, _ := e.tg.last(); !strings.Contains(text, "Прикреплено файлов: 1") {
		t.Errorf("reply:\n%s", text)
	}
	if len(e.ff.created) != 1 {
		t.Error("reply must not create a draft or transaction")
	}
}

func TestAlbumBecomesOneDraft(t *testing.T) {
	e := newEnv(t)
	e.tg.file = []byte("not a qr")
	done := make(chan struct{})
	go func() {
		defer close(done)
		time.Sleep(albumWait + 700*time.Millisecond)
	}()
	for i := 0; i < 3; i++ {
		msgSeq++
		e.b.Handle(context.Background(), &models.Update{Message: &models.Message{
			ID: msgSeq, From: &models.User{ID: ownerID}, Chat: models.Chat{ID: chatID, Type: models.ChatTypePrivate},
			MediaGroupID: "grp", Photo: []models.PhotoSize{{FileID: "f", FileUniqueID: "u" + string(rune('a'+i))}},
		}})
	}
	<-done
	e.b.mu.Lock()
	defer e.b.mu.Unlock()
	text, _ := e.tg.last()
	if !strings.Contains(text, "QR-код чека не найден") {
		t.Fatalf("album prompt:\n%s", text)
	}
	if n := len(e.tg.find("sendMessage")); n != 1 {
		t.Errorf("album produced %d messages", n)
	}
	sd, err := e.b.st.LatestDraft(context.Background(), ownerID)
	if err != nil {
		t.Fatal(err)
	}
	d, _ := decodeDraft(sd)
	if len(d.Files) != 3 {
		t.Errorf("files = %d", len(d.Files))
	}
}

func testLogOut() io.Writer {
	if os.Getenv("TSUKE_TEST_LOG") != "" {
		return os.Stderr
	}
	return io.Discard
}

func TestPhotoCaptionIsParsed(t *testing.T) {
	e := newEnv(t)
	e.tg.file = []byte("not a qr")
	e.sendPhoto("350 кофе #еда", 0)
	text, _ := e.tg.last()
	for _, want := range []string{"350", "кофе", "Еда", "Файлов: 1"} {
		if !strings.Contains(text, want) {
			t.Errorf("card lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "QR-код чека не найден") {
		t.Error("caption already has an amount; no need to ask")
	}
}
