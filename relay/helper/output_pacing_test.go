package helper

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakePacingClock advances only when the pacer sleeps. A non-nil gate holds
// every sleep until it is closed, and sleeping reports each sleep as it starts.
type fakePacingClock struct {
	mu       sync.Mutex
	now      time.Time
	gate     chan struct{}
	sleeping chan time.Duration
}

func (f *fakePacingClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fakePacingClock) Sleep(ctx context.Context, d time.Duration) error {
	if f.sleeping != nil {
		f.sleeping <- d
	}
	if f.gate != nil {
		select {
		case <-f.gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	f.Advance(d)
	return nil
}

func (f *fakePacingClock) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}

type pacedClientWrite struct {
	At   time.Duration
	Data string
}

// pacedClient is the client connection: it records when each write arrives
// on the fake clock.
type pacedClient struct {
	clock  *fakePacingClock
	start  time.Time
	header http.Header

	mu     sync.Mutex
	writes []pacedClientWrite
}

func (w *pacedClient) Header() http.Header { return w.header }

func (w *pacedClient) WriteHeader(int) {}

func (w *pacedClient) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.writes = append(w.writes, pacedClientWrite{At: w.clock.Now().Sub(w.start), Data: string(p)})
	return len(p), nil
}

func (w *pacedClient) Flush() {}

func (w *pacedClient) Writes() []pacedClientWrite {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]pacedClientWrite(nil), w.writes...)
}

func runeTokens(text string) int { return utf8.RuneCountInString(text) }

func newPacedTestContext(t *testing.T, setting dto.ChannelSettings) (*gin.Context, *pacedClient, *fakePacingClock, context.CancelFunc) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	clock := &fakePacingClock{now: time.Unix(1_700_000_000, 0)}
	client := &pacedClient{clock: clock, start: clock.now, header: http.Header{}}
	c, _ := gin.CreateTestContext(client)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(ctx)
	common.SetContextKey(c, constant.ContextKeyChannelSetting, setting)
	return c, client, clock, cancel
}

func chatChunk(content string) string {
	return `{"choices":[{"delta":{"content":"` + content + `"}}]}`
}

func TestOutputPacingReleasesStreamAtChannelRateWithoutBlockingUpstream(t *testing.T) {
	c, client, clock, _ := newPacedTestContext(t, dto.ChannelSettings{OutputTokensPerSecond: 10})
	clock.gate = make(chan struct{})
	pacer := startOutputPacing(c, clock, runeTokens)
	SetEventStreamHeaders(c)

	// Releasing the second event needs the gate, so the handler can only
	// finish while the gate is shut if upstream reads never wait on the client.
	handlerDone := make(chan struct{})
	go func() {
		defer close(handlerDone)
		for _, content := range []string{"aaaaa", "bbbbbbbbbb", "cccccccccccccccccccc"} {
			assert.NoError(t, StringData(c, chatChunk(content)))
		}
		Done(c)
	}()
	select {
	case <-handlerDone:
	case <-time.After(10 * time.Second):
		t.Fatal("upstream writes waited for paced client output")
	}

	close(clock.gate)
	pacer.Close()

	assert.Equal(t, []pacedClientWrite{
		{At: 0, Data: "data: " + chatChunk("aaaaa") + "\n\n"},
		{At: 500 * time.Millisecond, Data: "data: " + chatChunk("bbbbbbbbbb") + "\n\n"},
		{At: 1500 * time.Millisecond, Data: "data: " + chatChunk("cccccccccccccccccccc") + "\n\n"},
		{At: 3500 * time.Millisecond, Data: "data: [DONE]\n\n"},
	}, client.Writes())
}

func TestOutputPacingStopsWhenClientDisconnects(t *testing.T) {
	c, client, clock, cancel := newPacedTestContext(t, dto.ChannelSettings{OutputTokensPerSecond: 10})
	clock.gate = make(chan struct{})
	clock.sleeping = make(chan time.Duration, 4)
	pacer := startOutputPacing(c, clock, runeTokens)
	SetEventStreamHeaders(c)

	require.NoError(t, StringData(c, chatChunk("aaaaa")))
	require.NoError(t, StringData(c, chatChunk("bbbbb")))
	assert.Equal(t, 500*time.Millisecond, <-clock.sleeping)
	cancel()
	pacer.Close()

	assert.Equal(t, []pacedClientWrite{
		{At: 0, Data: "data: " + chatChunk("aaaaa") + "\n\n"},
	}, client.Writes())
}

func TestOutputPacingPassesStreamThroughUnpacedChannel(t *testing.T) {
	c, client, clock, _ := newPacedTestContext(t, dto.ChannelSettings{})
	pacer := startOutputPacing(c, clock, runeTokens)
	SetEventStreamHeaders(c)

	require.NoError(t, StringData(c, chatChunk("aaaaa")))
	var received strings.Builder
	for _, write := range client.Writes() {
		received.WriteString(write.Data)
	}
	assert.Equal(t, "data: "+chatChunk("aaaaa")+"\n\n", received.String())
	pacer.Close()
}

func TestOutputPacingHoldsNonStreamBody(t *testing.T) {
	tests := []struct {
		name         string
		setting      dto.ChannelSettings
		status       int
		body         string
		upstreamTime time.Duration
		wantAt       time.Duration
		wantHeld     bool
	}{
		{
			name:         "chat completion waits out the remaining time",
			setting:      dto.ChannelSettings{OutputTokensPerSecond: 10, OutputPacingNonStream: true},
			status:       http.StatusOK,
			body:         `{"usage":{"prompt_tokens":900,"completion_tokens":50}}`,
			upstreamTime: 2 * time.Second,
			wantAt:       5 * time.Second,
			wantHeld:     true,
		},
		{
			name:         "responses and claude output tokens",
			setting:      dto.ChannelSettings{OutputTokensPerSecond: 10, OutputPacingNonStream: true},
			status:       http.StatusOK,
			body:         `{"usage":{"input_tokens":900,"output_tokens":30}}`,
			upstreamTime: time.Second,
			wantAt:       3 * time.Second,
			wantHeld:     true,
		},
		{
			name:     "gemini counts candidate and thought tokens",
			setting:  dto.ChannelSettings{OutputTokensPerSecond: 10, OutputPacingNonStream: true},
			status:   http.StatusOK,
			body:     `{"usageMetadata":{"promptTokenCount":900,"candidatesTokenCount":20,"thoughtsTokenCount":10}}`,
			wantAt:   3 * time.Second,
			wantHeld: true,
		},
		{
			name:         "upstream slower than the rate adds no delay",
			setting:      dto.ChannelSettings{OutputTokensPerSecond: 10, OutputPacingNonStream: true},
			status:       http.StatusOK,
			body:         `{"usage":{"completion_tokens":10}}`,
			upstreamTime: 4 * time.Second,
			wantAt:       4 * time.Second,
			wantHeld:     true,
		},
		{
			name:         "error response is not held",
			setting:      dto.ChannelSettings{OutputTokensPerSecond: 10, OutputPacingNonStream: true},
			status:       http.StatusTooManyRequests,
			body:         `{"error":{"message":"busy"}}`,
			upstreamTime: 2 * time.Second,
			wantAt:       2 * time.Second,
		},
		{
			name:         "non-stream pacing off",
			setting:      dto.ChannelSettings{OutputTokensPerSecond: 10},
			status:       http.StatusOK,
			body:         `{"usage":{"completion_tokens":50}}`,
			upstreamTime: 2 * time.Second,
			wantAt:       2 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, client, clock, _ := newPacedTestContext(t, tt.setting)
			pacer := startOutputPacing(c, clock, runeTokens)

			clock.Advance(tt.upstreamTime)
			c.Data(tt.status, "application/json", []byte(tt.body))
			assert.Equal(t, tt.wantHeld, len(client.Writes()) == 0)
			pacer.Close()

			assert.Equal(t, []pacedClientWrite{{At: tt.wantAt, Data: tt.body}}, client.Writes())
		})
	}
}

func TestOutputPacingEventText(t *testing.T) {
	tests := []struct {
		name  string
		event string
		want  string
	}{
		{
			name:  "openai chat content reasoning and tool arguments",
			event: `data: {"choices":[{"delta":{"content":"ab","reasoning_content":"cd","tool_calls":[{"function":{"arguments":"ef"}}]}}]}` + "\n\n",
			want:  "abcdef",
		},
		{
			name:  "openai chat reasoning field",
			event: `data: {"choices":[{"delta":{"reasoning":"xy"}}]}` + "\n\n",
			want:  "xy",
		},
		{
			name:  "openai legacy completion",
			event: `data: {"choices":[{"text":"hi","index":0}]}` + "\n\n",
			want:  "hi",
		},
		{
			name:  "claude text delta",
			event: "event: content_block_delta\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}` + "\n\n",
			want:  "hi",
		},
		{
			name:  "claude thinking delta",
			event: "event: content_block_delta\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"hm"}}` + "\n\n",
			want:  "hm",
		},
		{
			name:  "claude tool input delta",
			event: "event: content_block_delta\n" + `data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"a\""}}` + "\n\n",
			want:  `{"a"`,
		},
		{
			name:  "claude message delta carries no output",
			event: "event: message_delta\n" + `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}` + "\n\n",
			want:  "",
		},
		{
			name:  "responses text delta",
			event: "event: response.output_text.delta\n" + `data: {"type":"response.output_text.delta","delta":"hi"}` + "\n\n",
			want:  "hi",
		},
		{
			name:  "responses completed repeats output and is not counted again",
			event: "event: response.completed\n" + `data: {"type":"response.completed","response":{"output":[{"content":[{"text":"full"}]}]}}` + "\n\n",
			want:  "",
		},
		{
			name:  "gemini parts",
			event: `data: {"candidates":[{"content":{"parts":[{"text":"hi"},{"text":"!","thought":true}]}}]}` + "\n\n",
			want:  "hi!",
		},
		{
			name:  "done marker",
			event: "data: [DONE]\n\n",
			want:  "",
		},
		{
			name:  "ping comment",
			event: ": PING\n\n",
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, outputPacingEventText([]byte(tt.event)))
		})
	}
}
