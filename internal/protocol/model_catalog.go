package protocol

// Model catalog: merges the live upstream model list (chatgpt.com
// /backend-api/models, fetched with an account token — this is how new
// OpenAI models are discovered automatically) with the static built-in
// fallbacks. The frontend loads this endpoint instead of hard-coded
// option lists, so newly released models show up without a redeploy.

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"chatgpt2api/internal/backend"
	"chatgpt2api/internal/util"
)

const (
	modelCatalogCacheTTL = 5 * time.Minute
)

type modelCatalogEntry struct {
	slug      string
	created   int64
	ownedBy   string
	fromLive  bool
	fromLocal bool
}

type modelCatalogCache struct {
	mu        sync.Mutex
	entries   []modelCatalogEntry
	fetchedAt time.Time
}

var sharedModelCatalog modelCatalogCache

// localModelSlugSet returns the built-in fallback slugs.
func localModelSlugSet() map[string]struct{} {
	set := make(map[string]struct{}, len(util.ModelList()))
	for _, slug := range util.ModelList() {
		set[slug] = struct{}{}
	}
	return set
}

// fetchLiveModels queries the upstream models endpoint through the account
// pool. Returns entries in upstream order; empty on failure (static list wins).
func (e *Engine) fetchLiveModels(ctx context.Context) []modelCatalogEntry {
	if e == nil || e.Accounts == nil {
		return nil
	}
	client := backend.NewClient("", e.Accounts, e.Proxy)
	result, err := client.ListModels(ctx)
	if err != nil {
		return nil
	}
	items := util.AsMapSlice(result["data"])
	entries := make([]modelCatalogEntry, 0, len(items))
	for _, item := range items {
		slug := util.Clean(item["id"])
		if slug == "" {
			continue
		}
		entries = append(entries, modelCatalogEntry{
			slug:     slug,
			created:  int64(util.ToInt(item["created"], 0)),
			ownedBy:  util.Clean(item["owned_by"]),
			fromLive: true,
		})
	}
	return entries
}

// BuildModelCatalog merges live + local model lists. When the live fetch
// fails, the previous cache (if any) is kept so the catalog stays stable.
func (e *Engine) BuildModelCatalog(ctx context.Context) map[string]any {
	sharedModelCatalog.mu.Lock()
	defer sharedModelCatalog.mu.Unlock()

	now := time.Now()
	if sharedModelCatalog.entries != nil && now.Sub(sharedModelCatalog.fetchedAt) < modelCatalogCacheTTL {
		return renderModelCatalog(sharedModelCatalog.entries, sharedModelCatalog.fetchedAt, "cache")
	}

	entries := e.fetchLiveModels(ctx)
	if len(entries) > 0 {
		sharedModelCatalog.entries = entries
		sharedModelCatalog.fetchedAt = now
		return renderModelCatalog(entries, now, "live")
	}
	if sharedModelCatalog.entries != nil {
		// Live fetch failed; keep serving the last good list.
		return renderModelCatalog(sharedModelCatalog.entries, sharedModelCatalog.fetchedAt, "cache")
	}
	return renderModelCatalog(nil, time.Time{}, "local")
}

func renderModelCatalog(live []modelCatalogEntry, fetchedAt time.Time, source string) map[string]any {
	local := localModelSlugSet()
	bySlug := make(map[string]*modelCatalogEntry, len(live)+len(local))
	order := make([]string, 0, len(live)+len(local))
	add := func(slug string, fromLive bool) {
		slug = strings.TrimSpace(slug)
		if slug == "" {
			return
		}
		if entry, ok := bySlug[slug]; ok {
			entry.fromLive = entry.fromLive || fromLive
			entry.fromLocal = entry.fromLocal || !fromLive
			return
		}
		entry := &modelCatalogEntry{slug: slug, fromLive: fromLive, fromLocal: !fromLive}
		if fromLive {
			for _, item := range live {
				if item.slug == slug {
					entry.created = item.created
					entry.ownedBy = item.ownedBy
					break
				}
			}
		}
		bySlug[slug] = entry
		order = append(order, slug)
	}
	for _, item := range live {
		add(item.slug, true)
	}
	for slug := range local {
		add(slug, false)
	}

	data := make([]map[string]any, 0, len(order))
	for _, slug := range order {
		entry := bySlug[slug]
		ownedBy := entry.ownedBy
		if ownedBy == "" {
			ownedBy = "chatgpt2api"
		}
		data = append(data, map[string]any{
			"id":         slug,
			"object":     "model",
			"created":    entry.created,
			"owned_by":   ownedBy,
			"permission": []any{},
			"root":       slug,
			"parent":     nil,
			"source": map[string]any{
				"live":  entry.fromLive,
				"local": entry.fromLocal,
			},
		})
	}
	sort.Slice(data, func(i, j int) bool { return util.Clean(data[i]["id"]) < util.Clean(data[j]["id"]) })

	var fetchedAtISO any
	if !fetchedAt.IsZero() {
		fetchedAtISO = fetchedAt.UTC().Format(time.RFC3339)
	}
	return map[string]any{
		"object":              "model_catalog",
		"schema_version":      1,
		"generated_at":        time.Now().UTC().Format(time.RFC3339),
		"fetched_at":          fetchedAtISO,
		"source":              source,
		"image_model_default": util.ImageModelGPT25,
		"data":                data,
	}
}

// InvalidateModelCatalog clears the cached catalog so the next request
// re-fetches the upstream list.
func InvalidateModelCatalog() {
	sharedModelCatalog.mu.Lock()
	defer sharedModelCatalog.mu.Unlock()
	sharedModelCatalog.entries = nil
	sharedModelCatalog.fetchedAt = time.Time{}
}
