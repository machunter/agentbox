package memory

import (
	"context"

	chromem "github.com/philippgille/chromem-go"
)

// Embedder turns text into vectors. Documents and queries are embedded through
// separate methods so models that retrieve best with task-specific prefixes
// (notably nomic-embed-text) can apply the right one to each.
type Embedder interface {
	EmbedDocument(ctx context.Context, text string) ([]float32, error)
	EmbedQuery(ctx context.Context, text string) ([]float32, error)
}

// DefaultOllamaModel is the local embedding model agentbox uses by default.
// It is small (~274MB), runs CPU-only, and has an 8K context — a good fit for
// embedding chunks of files and transcripts. Swapping it is a config change.
const DefaultOllamaModel = "nomic-embed-text"

// nomic-embed-text retrieves best when documents and queries carry distinct
// task prefixes. These are specific to that model family.
const (
	nomicDocPrefix   = "search_document: "
	nomicQueryPrefix = "search_query: "
)

// ollamaEmbedder embeds text via a local Ollama server. It is tuned for
// nomic-embed-text (applies the document/query prefixes that model expects).
type ollamaEmbedder struct {
	fn chromem.EmbeddingFunc
}

// NewOllamaEmbedder builds an Embedder backed by a local Ollama server, tuned
// for nomic-embed-text. If model is empty, DefaultOllamaModel is used; if
// baseURL is empty, chromem-go's default (http://localhost:11434/api) is used.
// All embedding stays on the machine — nothing is sent to a hosted API.
func NewOllamaEmbedder(model, baseURL string) Embedder {
	if model == "" {
		model = DefaultOllamaModel
	}
	return &ollamaEmbedder{fn: chromem.NewEmbeddingFuncOllama(model, baseURL)}
}

func (e *ollamaEmbedder) EmbedDocument(ctx context.Context, text string) ([]float32, error) {
	return e.fn(ctx, nomicDocPrefix+text)
}

func (e *ollamaEmbedder) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	return e.fn(ctx, nomicQueryPrefix+text)
}
