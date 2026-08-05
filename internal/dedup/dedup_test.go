package dedup

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/max-messenger/max-bot-api-client-go/v2/model"
)

func TestSeenOrMark_FirstSeenThenDuplicate(t *testing.T) {
	d := New(time.Minute, 0)
	assert.False(t, d.SeenOrMark("k1"), "первое — не дубль")
	assert.True(t, d.SeenOrMark("k1"), "второе — дубль")
	assert.True(t, d.SeenOrMark("k1"), "третье — дубль")
}

func TestSeenOrMark_DifferentKeys(t *testing.T) {
	d := New(time.Minute, 0)
	assert.False(t, d.SeenOrMark("a"))
	assert.False(t, d.SeenOrMark("b"))
	assert.True(t, d.SeenOrMark("a"))
	assert.True(t, d.SeenOrMark("b"))
}

func TestSeenOrMark_EmptyKeySkipped(t *testing.T) {
	d := New(time.Minute, 0)
	// Пустой ключ не дедуплится (всегда «уникально»).
	assert.False(t, d.SeenOrMark(""))
	assert.False(t, d.SeenOrMark(""))
}

func TestSeenOrMark_TTLExpiry(t *testing.T) {
	d := New(30*time.Millisecond, 0)
	assert.False(t, d.SeenOrMark("k"))
	time.Sleep(45 * time.Millisecond)
	assert.False(t, d.SeenOrMark("k"), "после истечения TTL снова уникально")
}

func TestSeenOrMark_LazyEvictionAtMaxSize(t *testing.T) {
	d := New(time.Minute, 3)
	d.SeenOrMark("a")
	d.SeenOrMark("b")
	d.SeenOrMark("c")
	// При достижении потолка протухших нет, но новая запись должна вписаться.
	assert.False(t, d.SeenOrMark("d"))
	assert.True(t, d.SeenOrMark("d"))
}

func TestKey_MessageCreated(t *testing.T) {
	k := Key(model.Update{UpdateType: model.UpdateMessageCreated, MessageID: "mid.abc.123"})
	assert.Equal(t, "m:mid.abc.123", k)
}

func TestKey_Callback(t *testing.T) {
	k := Key(model.Update{
		UpdateType: model.UpdateMessageCallback,
		Callback:   &model.Callback{CallbackID: "cb_42"},
	})
	assert.Equal(t, "c:cb_42", k)
}

func TestKey_CompositeForOtherEvents(t *testing.T) {
	k := Key(model.Update{UpdateType: model.UpdateBotStarted, UserID: 100, ChatID: 200, Timestamp: 9999})
	assert.Contains(t, k, "100")
	assert.Contains(t, k, "200")
	assert.Contains(t, k, "9999")
}

func TestKey_EmptyForBlankUpdate(t *testing.T) {
	assert.Empty(t, Key(model.Update{}))
}
