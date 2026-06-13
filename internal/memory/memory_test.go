package memory

import (
	"context"
	"hash/fnv"
	"strings"
	"testing"

	"google.golang.org/adk/memory"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

// fakeEmbedder is a deterministic, offline bag-of-words embedder: each word
// increments a fixed dimension, so texts that share words land near each other
// in cosine space. This lets the tests assert ranking without Ollama or a
// network. (chromem-go normalizes vectors itself, so raw counts are fine.)
//
// Documents and queries are embedded identically here — the nomic prefixes are
// the real Embedder's concern, not the store's.
type fakeEmbedder struct{}

const fakeDim = 64

func (fakeEmbedder) embed(text string) ([]float32, error) {
	vec := make([]float32, fakeDim)
	for word := range strings.FieldsSeq(strings.ToLower(text)) {
		h := fnv.New32a()
		_, _ = h.Write([]byte(word))
		vec[h.Sum32()%fakeDim] += 1
	}
	// Guard against an all-zero vector (chromem can't normalize it).
	allZero := true
	for _, v := range vec {
		if v != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		vec[0] = 1
	}
	return vec, nil
}

func (e fakeEmbedder) EmbedDocument(_ context.Context, text string) ([]float32, error) {
	return e.embed(text)
}

func (e fakeEmbedder) EmbedQuery(_ context.Context, text string) ([]float32, error) {
	return e.embed(text)
}

// buildSession creates an ADK session and appends one model event per text.
func buildSession(t *testing.T, ctx context.Context, svc session.Service, app, user, sid string, texts ...string) session.Session {
	t.Helper()
	createResp, err := svc.Create(ctx, &session.CreateRequest{AppName: app, UserID: user, SessionID: sid})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	sess := createResp.Session
	for _, text := range texts {
		ev := session.NewEvent("inv-" + sid)
		ev.Author = "agentbox"
		ev.LLMResponse.Content = genai.NewContentFromText(text, genai.RoleModel)
		if err := svc.AppendEvent(ctx, sess, ev); err != nil {
			t.Fatalf("append event: %v", err)
		}
	}
	return sess
}

func newTestService(t *testing.T) *Service {
	t.Helper()
	s, err := New(Config{Embedder: fakeEmbedder{}}) // empty DBPath -> in-memory
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func TestSearchEmptyStore(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)

	resp, err := s.SearchMemory(ctx, &memory.SearchRequest{Query: "anything", AppName: "agentbox", UserID: "u1"})
	if err != nil {
		t.Fatalf("SearchMemory: %v", err)
	}
	if len(resp.Memories) != 0 {
		t.Fatalf("want 0 memories from empty store, got %d", len(resp.Memories))
	}
}

func TestEmptyQueryReturnsNothing(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	sessSvc := session.InMemoryService()
	sess := buildSession(t, ctx, sessSvc, "agentbox", "u1", "s1", "the cat sat on the mat")
	if err := s.AddSessionToMemory(ctx, sess); err != nil {
		t.Fatalf("AddSessionToMemory: %v", err)
	}

	resp, err := s.SearchMemory(ctx, &memory.SearchRequest{Query: "   ", AppName: "agentbox", UserID: "u1"})
	if err != nil {
		t.Fatalf("SearchMemory: %v", err)
	}
	if len(resp.Memories) != 0 {
		t.Fatalf("blank query should match nothing, got %d", len(resp.Memories))
	}
}

func TestAddAndSearchRanksRelevant(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	sessSvc := session.InMemoryService()

	sess := buildSession(t, ctx, sessSvc, "agentbox", "u1", "s1",
		"the database connection string lives in vault",
		"my dentist appointment is on tuesday afternoon",
		"remember to water the office plants weekly",
	)
	if err := s.AddSessionToMemory(ctx, sess); err != nil {
		t.Fatalf("AddSessionToMemory: %v", err)
	}

	resp, err := s.SearchMemory(ctx, &memory.SearchRequest{
		Query:   "where is the database connection string",
		AppName: "agentbox",
		UserID:  "u1",
	})
	if err != nil {
		t.Fatalf("SearchMemory: %v", err)
	}
	if len(resp.Memories) == 0 {
		t.Fatal("expected at least one memory")
	}
	top := resp.Memories[0]
	if got := textOf(top.Content); !strings.Contains(got, "database connection string") {
		t.Fatalf("top result not the relevant memory; got %q", got)
	}
	if top.Author != "agentbox" {
		t.Errorf("author not preserved: got %q", top.Author)
	}
}

func TestTopKCap(t *testing.T) {
	ctx := context.Background()
	s, err := New(Config{Embedder: fakeEmbedder{}, TopK: 2})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sessSvc := session.InMemoryService()
	sess := buildSession(t, ctx, sessSvc, "agentbox", "u1", "s1",
		"alpha one", "alpha two", "alpha three", "alpha four", "alpha five")
	if err := s.AddSessionToMemory(ctx, sess); err != nil {
		t.Fatalf("AddSessionToMemory: %v", err)
	}

	resp, err := s.SearchMemory(ctx, &memory.SearchRequest{Query: "alpha", AppName: "agentbox", UserID: "u1"})
	if err != nil {
		t.Fatalf("SearchMemory: %v", err)
	}
	if len(resp.Memories) != 2 {
		t.Fatalf("TopK=2 should cap results at 2, got %d", len(resp.Memories))
	}
}

func TestUserIsolation(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	sessSvc := session.InMemoryService()

	sessA := buildSession(t, ctx, sessSvc, "agentbox", "alice", "sa", "alice secret project falcon")
	sessB := buildSession(t, ctx, sessSvc, "agentbox", "bob", "sb", "bob secret project falcon")
	if err := s.AddSessionToMemory(ctx, sessA); err != nil {
		t.Fatalf("add A: %v", err)
	}
	if err := s.AddSessionToMemory(ctx, sessB); err != nil {
		t.Fatalf("add B: %v", err)
	}

	resp, err := s.SearchMemory(ctx, &memory.SearchRequest{Query: "secret project falcon", AppName: "agentbox", UserID: "alice"})
	if err != nil {
		t.Fatalf("SearchMemory: %v", err)
	}
	for _, m := range resp.Memories {
		if strings.Contains(textOf(m.Content), "bob") {
			t.Fatalf("alice's search leaked bob's memory: %q", textOf(m.Content))
		}
	}
	if len(resp.Memories) == 0 {
		t.Fatal("expected alice's own memory back")
	}
}

func textOf(c *genai.Content) string {
	if c == nil {
		return ""
	}
	var b strings.Builder
	for _, p := range c.Parts {
		b.WriteString(p.Text)
	}
	return b.String()
}
