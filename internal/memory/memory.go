// Package memory is a self-hosted, fully local implementation of ADK's
// memory.Service. It stores session content as embeddings in an embedded
// chromem-go vector database and retrieves them by semantic similarity, so the
// agent can recall context across runs without anything leaving the machine.
//
// Embeddings are produced locally (see Embedder); the vector store is in-process
// (no separate database service). The store is namespaced so that, for example,
// a personal deployment and a work deployment can never read each other's
// memories.
package memory

import (
	"context"
	"fmt"
	"strings"
	"time"

	chromem "github.com/philippgille/chromem-go"
	"google.golang.org/adk/memory"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

const (
	defaultTopK    = 5
	collectionBase = "memory"
)

// Service implements google.golang.org/adk/memory.Service over chromem-go.
type Service struct {
	coll     *chromem.Collection
	embedder Embedder
	topK     int
}

// Compile-time check that Service satisfies the ADK interface.
var _ memory.Service = (*Service)(nil)

// Config configures a memory Service.
type Config struct {
	// Namespace isolates one deployment's memory from another (e.g. "personal"
	// vs "work"). It becomes part of the on-disk collection name, so two
	// namespaces can never read each other's memories. Defaults to "default".
	Namespace string
	// DBPath is the directory for the persistent vector store. If empty, an
	// in-memory store is used (handy for tests).
	DBPath string
	// Embedder produces vectors for documents and queries. Required.
	Embedder Embedder
	// TopK caps how many memories a search returns. Defaults to 5.
	TopK int
}

// New opens (or creates) the memory store described by cfg.
func New(cfg Config) (*Service, error) {
	if cfg.Embedder == nil {
		return nil, fmt.Errorf("memory: Embedder is required")
	}
	ns := cfg.Namespace
	if ns == "" {
		ns = "default"
	}
	topK := cfg.TopK
	if topK <= 0 {
		topK = defaultTopK
	}

	var (
		db  *chromem.DB
		err error
	)
	if cfg.DBPath == "" {
		db = chromem.NewDB()
	} else if db, err = chromem.NewPersistentDB(cfg.DBPath, false); err != nil {
		return nil, fmt.Errorf("memory: open persistent db: %w", err)
	}

	// We always supply precomputed embeddings to Add/QueryEmbedding, so the
	// collection's own embedding func is never invoked; pass the query embedder
	// as a harmless fallback to satisfy the API.
	fallback := func(ctx context.Context, text string) ([]float32, error) {
		return cfg.Embedder.EmbedQuery(ctx, text)
	}
	coll, err := db.GetOrCreateCollection(collectionBase+"_"+ns, nil, fallback)
	if err != nil {
		return nil, fmt.Errorf("memory: open collection: %w", err)
	}

	return &Service{coll: coll, embedder: cfg.Embedder, topK: topK}, nil
}

// AddSessionToMemory embeds each text-bearing event of the session and stores
// it. A session may be added multiple times; events keep stable IDs so re-adds
// overwrite rather than duplicate.
func (s *Service) AddSessionToMemory(ctx context.Context, sess session.Session) error {
	app, user, sid := sess.AppName(), sess.UserID(), sess.ID()

	var (
		ids      []string
		embeds   [][]float32
		metas    []map[string]string
		contents []string
	)

	i := -1
	for event := range sess.Events().All() {
		i++
		if event.LLMResponse.Content == nil {
			continue
		}
		text := joinText(event.LLMResponse.Content)
		if text == "" {
			continue
		}

		vec, err := s.embedder.EmbedDocument(ctx, text)
		if err != nil {
			return fmt.Errorf("memory: embed event: %w", err)
		}

		id := event.ID
		if id == "" {
			id = fmt.Sprintf("%s/%d", sid, i)
		}
		ids = append(ids, id)
		embeds = append(embeds, vec)
		metas = append(metas, map[string]string{
			"app":       app,
			"user":      user,
			"session":   sid,
			"author":    event.Author,
			"timestamp": event.Timestamp.UTC().Format(time.RFC3339),
		})
		contents = append(contents, text)
	}

	if len(ids) == 0 {
		return nil
	}
	if err := s.coll.Add(ctx, ids, embeds, metas, contents); err != nil {
		return fmt.Errorf("memory: add documents: %w", err)
	}
	return nil
}

// SearchMemory returns memories relevant to the query, scoped to the request's
// app and user. An empty query or empty store yields no matches (not an error).
func (s *Service) SearchMemory(ctx context.Context, req *memory.SearchRequest) (*memory.SearchResponse, error) {
	resp := &memory.SearchResponse{}
	if req == nil || strings.TrimSpace(req.Query) == "" {
		return resp, nil
	}

	// chromem errors if nResults exceeds the total document count, so clamp it
	// (and short-circuit an empty store).
	count := s.coll.Count()
	if count == 0 {
		return resp, nil
	}
	k := min(s.topK, count)

	vec, err := s.embedder.EmbedQuery(ctx, req.Query)
	if err != nil {
		return nil, fmt.Errorf("memory: embed query: %w", err)
	}

	where := map[string]string{}
	if req.AppName != "" {
		where["app"] = req.AppName
	}
	if req.UserID != "" {
		where["user"] = req.UserID
	}
	if len(where) == 0 {
		where = nil
	}

	results, err := s.coll.QueryEmbedding(ctx, vec, k, where, nil)
	if err != nil {
		return nil, fmt.Errorf("memory: query: %w", err)
	}

	for _, r := range results {
		resp.Memories = append(resp.Memories, memory.Entry{
			ID:        r.ID,
			Content:   genai.NewContentFromText(r.Content, genai.RoleModel),
			Author:    r.Metadata["author"],
			Timestamp: parseTime(r.Metadata["timestamp"]),
			CustomMetadata: map[string]any{
				"session":    r.Metadata["session"],
				"similarity": r.Similarity,
			},
		})
	}
	return resp, nil
}

// joinText concatenates an event's user-visible text parts, skipping the
// model's thinking and non-text parts (tool calls/responses).
func joinText(c *genai.Content) string {
	var b strings.Builder
	for _, p := range c.Parts {
		if p == nil || p.Thought || p.Text == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(p.Text)
	}
	return b.String()
}

// parseTime parses an RFC3339 timestamp, returning the zero time on failure.
func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}
