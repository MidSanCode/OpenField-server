package repository

import (
	"fmt"
	"strings"

	"github.com/lib/pq"
	"github.com/openfield/server/pkg/database"
)

// Post tags are free-form short labels authored on a post. They live in their
// own table (not a TEXT[] on the posts row) so the hot feed query stays
// simple and tag edits only touch a handful of rows.

// SetPostTags replaces the post's tag set with the supplied list. Empty lists
// are a no-op rather than a delete-all so a bot accidentally publishing [] does
// not wipe an editor's curated tags.
func SetPostTags(postID int64, tags []string) error {
	tx, err := database.DB.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin tags tx: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec("DELETE FROM post_tags WHERE post_id = $1", postID); err != nil {
		return fmt.Errorf("failed to clear post tags: %w", err)
	}
	if len(tags) > 0 {
		clean := normalizeTags(tags)
		if len(clean) > 0 {
			rows := make([][]any, 0, len(clean))
			for _, t := range clean {
				rows = append(rows, []any{postID, t})
			}
			// Bulk insert with ON CONFLICT to dedupe against any in-flight row.
			_, err := tx.Exec(
				`INSERT INTO post_tags (post_id, tag) SELECT * FROM UNNEST($1::bigint[], $2::text[]) AS t
				 ON CONFLICT DO NOTHING`,
				pq.Array(extractFirst(rows)), pq.Array(extractSecond(rows)),
			)
			if err != nil {
				return fmt.Errorf("failed to insert post tags: %w", err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit post tags: %w", err)
	}
	return nil
}

// LoadPostTagsFor returns the tag list for each requested post id.
func LoadPostTagsFor(postIDs []int64) (map[int64][]string, error) {
	if len(postIDs) == 0 {
		return map[int64][]string{}, nil
	}
	rows, err := database.DB.Query(
		"SELECT post_id, tag FROM post_tags WHERE post_id = ANY($1) ORDER BY tag",
		pq.Array(postIDs),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to load post tags: %w", err)
	}
	defer rows.Close()
	out := map[int64][]string{}
	for rows.Next() {
		var id int64
		var tag string
		if err := rows.Scan(&id, &tag); err != nil {
			return nil, err
		}
		out[id] = append(out[id], tag)
	}
	return out, rows.Err()
}

// ListPostsByTag returns post ids carrying the given tag, newest first.
func ListPostsByTag(tag string, limit int) ([]int64, error) {
	if limit < 1 {
		limit = 50
	}
	rows, err := database.DB.Query(
		"SELECT post_id FROM post_tags WHERE tag = $1 ORDER BY post_id DESC LIMIT $2",
		tag, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query posts by tag: %w", err)
	}
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// normalizeTags trims, lowercases, dedupes and length-caps incoming tags.
func normalizeTags(in []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, t := range in {
		t = strings.TrimSpace(t)
		t = strings.TrimPrefix(t, "#")
		if t == "" {
			continue
		}
		if len(t) > 64 {
			t = t[:64]
		}
		t = strings.ToLower(t)
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	if len(out) > 20 {
		out = out[:20]
	}
	return out
}

// helpers for the bulk insert in SetPostTags (avoids a [][]any in callers).
func extractFirst(rows [][]any) []int64 {
	out := make([]int64, len(rows))
	for i, r := range rows {
		out[i] = r[0].(int64)
	}
	return out
}
func extractSecond(rows [][]any) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r[1].(string)
	}
	return out
}
