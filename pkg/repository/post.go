package repository

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/lib/pq"
	"github.com/openfield/server/pkg/database"
	"github.com/openfield/server/pkg/logger"
	"github.com/openfield/server/pkg/model"
)

// PostRepository handles post-related database operations.
type PostRepository struct {
	attachmentRepo *AttachmentRepository
}

// authorMemberCols is the denormalized member/name-style portion of a user
// JOIN, kept in sync with the model.Post/Member fields scanned alongside it.
// It is appended after u.is_verified so every post query carries the author's
// bot flag, membership tier and display-name styling to the client.
const authorMemberCols = ", u.is_bot, u.member_level, u.member_expires_at, u.name_color, u.name_color_to, u.name_dynamic, u.name_colors, u.name_gradient_direction, u.avatar_frame"

// tipTotalExpr is a correlated subquery summing the non-refunded net tip
// amount (cents) for a post. It is appended after the favorite-count subquery
// in every post SELECT and scanned last into model.Post.TipTotal.
const tipTotalExpr = `(SELECT COALESCE(SUM(pt.net_amount), 0) FROM post_tips pt WHERE pt.post_id = p.id AND pt.refunded_at IS NULL) AS tip_total`

// applyMemberStatus fills the denormalized active-member flag from the scanned
// level/expiry so callers don't recompute it in every query site.
func applyMemberStatus(memberLevel *int64, memberExpiresAt **time.Time, memberActive *bool) {
	*memberActive = *memberLevel > 0 && *memberExpiresAt != nil && time.Now().Before(**memberExpiresAt)
}

// NewPostRepository creates a new PostRepository.
func NewPostRepository() *PostRepository {
	return &PostRepository{
		attachmentRepo: NewAttachmentRepository(),
	}
}

// scanPosts drains a post row set using the shared SELECT column layout.
func scanPosts(rows *sql.Rows) ([]model.Post, []int64, error) {
	posts := make([]model.Post, 0)
	postIDs := make([]int64, 0)
	for rows.Next() {
		var post model.Post
		if err := rows.Scan(&post.ID, &post.UserID, &post.Content, &post.Visibility, &post.CreatedAt, &post.UpdatedAt, &post.QuotedPostID, &post.Pinned, &post.CampID, &post.Username, &post.Nickname, &post.AvatarURL, &post.IsVerified, &post.IsBot, &post.MemberLevel, &post.MemberExpiresAt, &post.NameColor, &post.NameColorTo, &post.NameDynamic, &post.NameColors, &post.NameGradientDirection, &post.AvatarFrame, &post.ReplyCount, &post.FavoriteCount, &post.TipTotal); err != nil {
			return nil, nil, fmt.Errorf("failed to scan post: %w", err)
		}
		applyMemberStatus(&post.MemberLevel, &post.MemberExpiresAt, &post.MemberActive)
		posts = append(posts, post)
		postIDs = append(postIDs, post.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("rows error: %w", err)
	}
	return posts, postIDs, nil
}

// visibilityCondition returns the SQL predicate limiting posts to those the
// viewer may see, referencing the viewer id as the given placeholder (e.g.
// "$2"). The placeholder must hold the viewer id (0 for anonymous readers) and
// is always referenced with an explicit ::bigint cast so its type is
// deterministic regardless of the surrounding query.
func visibilityCondition(viewerID int64, prefix, viewerParam string) string {
	if viewerID <= 0 {
		// Anonymous viewers may only see public posts. The viewer param is
		// still referenced (guaranteed 0) so PostgreSQL can infer its type.
		return fmt.Sprintf("(%svisibility = 'public' AND (%s::bigint = 0))", prefix, viewerParam)
	}
	return fmt.Sprintf(`(%[1]suser_id = %[2]s
		OR %[1]svisibility = 'public'
		OR %[1]svisibility = 'login'
		OR (%[1]svisibility = 'friends' AND EXISTS (
			SELECT 1 FROM user_follows f1
			JOIN user_follows f2 ON f1.follower_id = f2.followee_id AND f2.follower_id = f1.followee_id
			WHERE f1.follower_id = %[2]s AND f1.followee_id = %[1]suser_id
		)))`, prefix, viewerParam)
}

// Create creates a new post with optional attachments, an optional
// attached check ([checkID] > 0 binds an active unattached check owned by the
// same user) and an optional quoted post ([quotedPostID] > 0 makes the post a
// quote/repost; an empty content with only the reference is a pure repost).
func (r *PostRepository) Create(userID int64, content, visibility string, attachmentIDs []int64, checkID int64, quotedPostID int64, campID int64) (*model.Post, error) {
	post := &model.Post{}
	err := database.DB.QueryRow(
		"INSERT INTO posts (user_id, content, visibility, quoted_post_id, camp_id) VALUES ($1, $2, $3, NULLIF($4, 0), NULLIF($5, 0)) RETURNING id, user_id, content, visibility, COALESCE(quoted_post_id, 0), created_at, updated_at",
		userID, content, visibility, quotedPostID, campID,
	).Scan(&post.ID, &post.UserID, &post.Content, &post.Visibility, &post.QuotedPostID, &post.CreatedAt, &post.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to create post: %w", err)
	}
	post.CampID = campID

	if len(attachmentIDs) > 0 {
		if err := r.attachmentRepo.AttachToPost(post.ID, attachmentIDs); err != nil {
			return nil, err
		}
	}

	if checkID > 0 {
		checkRepo := NewCheckRepository()
		if err := checkRepo.AttachToPost(checkID, userID, post.ID); err != nil {
			return nil, err
		}
	}

	post.Attachments, err = r.attachmentRepo.GetByPostID(post.ID)
	if err != nil {
		return nil, err
	}
	post.ReplyCount, err = r.replyCount(post.ID)
	if err != nil {
		return nil, err
	}
	if err := r.populateStats([]model.Post{*post}, []int64{post.ID}); err != nil {
		return nil, err
	}
	if err := populateChecks([]model.Post{*post}, []int64{post.ID}, userID); err != nil {
		return nil, err
	}
	if err := populateQuotedPost(post, userID); err != nil {
		return nil, err
	}
	return post, nil
}

// populateQuotedPost resolves the quoted-post reference on a single post and
// writes the result back into [post] (the slice variant mutates a copy, so
// single-post callers must go through this wrapper).
func populateQuotedPost(post *model.Post, viewerID int64) error {
	if post == nil || post.QuotedPostID <= 0 {
		return nil
	}
	batch := []model.Post{*post}
	if err := populateQuotedPosts(batch, viewerID); err != nil {
		return err
	}
	*post = batch[0]
	return nil
}

// GetByID retrieves a post by ID with author info and attachments.
func (r *PostRepository) GetByID(id int64) (*model.Post, error) {
	post := &model.Post{}
	err := database.DB.QueryRow(
		`SELECT p.id, p.user_id, p.content, p.visibility, p.created_at, p.updated_at, COALESCE(p.quoted_post_id, 0), p.pinned, COALESCE(p.camp_id, 0), u.username, u.nickname, u.avatar_url, u.is_verified`+authorMemberCols+`,
		        (SELECT COUNT(*) FROM post_favorites pf WHERE pf.post_id = p.id) AS favorite_count,
		        `+tipTotalExpr+`
		 FROM posts p
		 JOIN users u ON p.user_id = u.id
		 WHERE p.id = $1`,
		id,
	).Scan(&post.ID, &post.UserID, &post.Content, &post.Visibility, &post.CreatedAt, &post.UpdatedAt, &post.QuotedPostID, &post.Pinned, &post.CampID, &post.Username, &post.Nickname, &post.AvatarURL, &post.IsVerified, &post.IsBot, &post.MemberLevel, &post.MemberExpiresAt, &post.NameColor, &post.NameColorTo, &post.NameDynamic, &post.NameColors, &post.NameGradientDirection, &post.AvatarFrame, &post.FavoriteCount, &post.TipTotal)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	applyMemberStatus(&post.MemberLevel, &post.MemberExpiresAt, &post.MemberActive)

	post.Attachments, err = r.attachmentRepo.GetByPostID(post.ID)
	if err != nil {
		return nil, err
	}
	post.ReplyCount, err = r.replyCount(post.ID)
	if err != nil {
		return nil, err
	}
	if err := r.populateStats([]model.Post{*post}, []int64{post.ID}); err != nil {
		return nil, err
	}
	if err := populateChecks([]model.Post{*post}, []int64{post.ID}, 0); err != nil {
		return nil, err
	}
	return post, nil
}

// GetByIDWithViewer retrieves a post by ID, additionally populating the
// viewer's own reaction and favorite state so the client can render active state.
func (r *PostRepository) GetByIDWithViewer(id, viewerID int64) (*model.Post, error) {
	post, err := r.GetByID(id)
	if err != nil || post == nil {
		return post, err
	}
	// Re-resolve the quoted post against this viewer: GetByID ran with an
	// anonymous viewer, so a quote of a friends-only post would have been
	// masked for an entitled viewer.
	if post.QuotedPostID > 0 {
		if err := populateQuotedPost(post, viewerID); err != nil {
			return nil, err
		}
	}
	if viewerID > 0 {
		post.MyReaction, err = r.GetMyReaction(post.ID, viewerID)
		if err != nil {
			return nil, err
		}
		post.Favorited, err = r.IsPostFavorited(post.ID, viewerID)
		if err != nil {
			return nil, err
		}
		if post.Check != nil {
			if err := populateChecks([]model.Post{*post}, []int64{post.ID}, viewerID); err != nil {
				return nil, err
			}
		}
	}
	return post, nil
}

// RecordView registers a view of a post. The total counter always increments;
// unique views are tracked per (post, viewer_key) so repeat visits by the same
// viewer do not inflate the unique count.
func (r *PostRepository) RecordView(postID int64, viewerKey string) error {
	if _, err := database.DB.Exec(
		"UPDATE posts SET view_count = view_count + 1 WHERE id = $1", postID,
	); err != nil {
		return fmt.Errorf("failed to increment view count: %w", err)
	}
	if _, err := database.DB.Exec(
		"INSERT INTO post_views (post_id, viewer_key) VALUES ($1, $2) ON CONFLICT (post_id, viewer_key) DO NOTHING",
		postID, viewerKey,
	); err != nil {
		return fmt.Errorf("failed to record unique view: %w", err)
	}
	return nil
}

// SetReaction upserts the authenticated user's reaction on a post.
func (r *PostRepository) SetReaction(postID, userID int64, reaction string) error {
	if _, err := database.DB.Exec(
		`INSERT INTO post_reactions (post_id, user_id, reaction) VALUES ($1, $2, $3)
		 ON CONFLICT (post_id, user_id) DO UPDATE SET reaction = EXCLUDED.reaction`,
		postID, userID, reaction,
	); err != nil {
		return fmt.Errorf("failed to set reaction: %w", err)
	}
	return nil
}

// RemoveReaction clears the authenticated user's reaction on a post.
func (r *PostRepository) RemoveReaction(postID, userID int64) error {
	if _, err := database.DB.Exec(
		"DELETE FROM post_reactions WHERE post_id = $1 AND user_id = $2", postID, userID,
	); err != nil {
		return fmt.Errorf("failed to remove reaction: %w", err)
	}
	return nil
}

// GetMyReaction returns the user's reaction for a post ("" when none).
func (r *PostRepository) GetMyReaction(postID, userID int64) (string, error) {
	var reaction string
	err := database.DB.QueryRow(
		"SELECT reaction FROM post_reactions WHERE post_id = $1 AND user_id = $2", postID, userID,
	).Scan(&reaction)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("failed to get my reaction: %w", err)
	}
	return reaction, nil
}

// populateStats fills view counts and reaction tallies for the given posts.
func (r *PostRepository) populateStats(posts []model.Post, postIDs []int64) error {
	if len(postIDs) == 0 {
		return nil
	}
	byID := make(map[int64]*model.Post, len(posts))
	for i := range posts {
		byID[posts[i].ID] = &posts[i]
	}

	// Unique views per post.
	rows, err := database.DB.Query(
		"SELECT post_id, COUNT(*) FROM post_views WHERE post_id = ANY($1) GROUP BY post_id",
		pq.Array(postIDs),
	)
	if err != nil {
		return fmt.Errorf("failed to count unique views: %w", err)
	}
	for rows.Next() {
		var postID, count int64
		if err := rows.Scan(&postID, &count); err != nil {
			rows.Close()
			return fmt.Errorf("failed to scan unique view count: %w", err)
		}
		if p, ok := byID[postID]; ok {
			p.UniqueViews = count
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("rows error: %w", err)
	}

	// Total views stored on the posts table.
	rows, err = database.DB.Query(
		"SELECT id, view_count FROM posts WHERE id = ANY($1)", pq.Array(postIDs),
	)
	if err != nil {
		return fmt.Errorf("failed to query view counts: %w", err)
	}
	for rows.Next() {
		var postID, count int64
		if err := rows.Scan(&postID, &count); err != nil {
			rows.Close()
			return fmt.Errorf("failed to scan view count: %w", err)
		}
		if p, ok := byID[postID]; ok {
			p.ViewCount = count
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("rows error: %w", err)
	}

	// Reaction tallies per post.
	rows, err = database.DB.Query(
		"SELECT post_id, reaction, COUNT(*) FROM post_reactions WHERE post_id = ANY($1) GROUP BY post_id, reaction",
		pq.Array(postIDs),
	)
	if err != nil {
		return fmt.Errorf("failed to count reactions: %w", err)
	}
	for rows.Next() {
		var postID, count int64
		var reaction string
		if err := rows.Scan(&postID, &reaction, &count); err != nil {
			rows.Close()
			return fmt.Errorf("failed to scan reaction count: %w", err)
		}
		if p, ok := byID[postID]; ok {
			if p.Reactions == nil {
				p.Reactions = make(map[string]int64)
			}
			p.Reactions[reaction] = count
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("rows error: %w", err)
	}
	return nil
}

// List retrieves paginated posts with author info, attachments and reply counts,
// restricted to posts the viewer may see based on their visibility.
func (r *PostRepository) List(page, limit int, viewerID int64) ([]model.Post, error) {
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 20
	}
	offset := (page - 1) * limit

	rows, err := database.DB.Query(
		`SELECT p.id, p.user_id, p.content, p.visibility, p.created_at, p.updated_at, COALESCE(p.quoted_post_id, 0), p.pinned, COALESCE(p.camp_id, 0), u.username, u.nickname, u.avatar_url, u.is_verified`+authorMemberCols+`,
		        (SELECT COUNT(*) FROM post_replies pr WHERE pr.post_id = p.id AND pr.deleted_at IS NULL) AS reply_count,
		        (SELECT COUNT(*) FROM post_favorites pf WHERE pf.post_id = p.id) AS favorite_count,
		        `+tipTotalExpr+`
		 FROM posts p
		 JOIN users u ON p.user_id = u.id
		 WHERE p.camp_id IS NULL AND `+visibilityCondition(viewerID, "p.", "$1")+`
		 ORDER BY p.pinned DESC, p.created_at DESC
		 LIMIT $2 OFFSET $3`,
		viewerID, limit, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list posts: %w", err)
	}
	defer rows.Close()

	posts := make([]model.Post, 0)
	var postIDs []int64
	for rows.Next() {
		var post model.Post
		if err := rows.Scan(&post.ID, &post.UserID, &post.Content, &post.Visibility, &post.CreatedAt, &post.UpdatedAt, &post.QuotedPostID, &post.Pinned, &post.CampID, &post.Username, &post.Nickname, &post.AvatarURL, &post.IsVerified, &post.IsBot, &post.MemberLevel, &post.MemberExpiresAt, &post.NameColor, &post.NameColorTo, &post.NameDynamic, &post.NameColors, &post.NameGradientDirection, &post.AvatarFrame, &post.ReplyCount, &post.FavoriteCount, &post.TipTotal); err != nil {
			return nil, fmt.Errorf("failed to scan post: %w", err)
		}
		applyMemberStatus(&post.MemberLevel, &post.MemberExpiresAt, &post.MemberActive)
		posts = append(posts, post)
		postIDs = append(postIDs, post.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}

	if err := r.populateAttachments(posts, postIDs); err != nil {
		return nil, err
	}
	if err := r.populateStats(posts, postIDs); err != nil {
		return nil, err
	}
	if err := r.populateFavorited(posts, postIDs, viewerID); err != nil {
		return nil, err
	}
	if err := populateChecks(posts, postIDs, viewerID); err != nil {
		return nil, err
	}
	if err := populateQuotedPosts(posts, viewerID); err != nil {
		return nil, err
	}
	return posts, nil
}

// PostSearchFilter narrows a post listing/search. Zero values are ignored;
// all criteria combine with AND. Query is a case-insensitive content
// substring; AuthorID filters by author; AuthorName matches username or
// nickname substrings; From/To bound created_at inclusively.
type PostSearchFilter struct {
	Query      string
	AuthorID   int64
	AuthorName string
	From       *time.Time
	To         *time.Time
	Tag        string
}

// searchQuery builds the shared post SELECT with the visibility rule and any
// active [f] criteria. Args are appended in placeholder order starting from
// the given next index; it returns the WHERE clause fragment.
func buildPostFilter(f PostSearchFilter) (string, []interface{}) {
	where := "TRUE"
	args := make([]interface{}, 0, 5)
	if f.Query != "" {
		args = append(args, "%"+f.Query+"%")
		where += fmt.Sprintf(" AND p.content ILIKE $%d", len(args))
	}
	if f.AuthorID > 0 {
		args = append(args, f.AuthorID)
		where += fmt.Sprintf(" AND p.user_id = $%d", len(args))
	}
	if f.AuthorName != "" {
		args = append(args, "%"+f.AuthorName+"%")
		n := len(args)
		where += fmt.Sprintf(" AND (u.username ILIKE $%d OR u.nickname ILIKE $%d)", n, n)
	}
	if f.From != nil {
		args = append(args, *f.From)
		where += fmt.Sprintf(" AND p.created_at >= $%d", len(args))
	}
	if f.To != nil {
		args = append(args, *f.To)
		where += fmt.Sprintf(" AND p.created_at <= $%d", len(args))
	}
	return where, args
}

// Search retrieves paginated posts matching the filter, restricted to posts
// the viewer may see based on their visibility.
func (r *PostRepository) Search(f PostSearchFilter, page, limit int, viewerID int64) ([]model.Post, error) {
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 20
	}
	offset := (page - 1) * limit
	where, args := buildPostFilter(f)
	// Visibility takes the first placeholder.
	args = append([]interface{}{viewerID}, args...)
	visibility := visibilityCondition(viewerID, "p.", "$1")

	query := `SELECT p.id, p.user_id, p.content, p.visibility, p.created_at, p.updated_at, COALESCE(p.quoted_post_id, 0), p.pinned, COALESCE(p.camp_id, 0), u.username, u.nickname, u.avatar_url, u.is_verified` + authorMemberCols + `,
	        (SELECT COUNT(*) FROM post_replies pr WHERE pr.post_id = p.id AND pr.deleted_at IS NULL) AS reply_count,
	        (SELECT COUNT(*) FROM post_favorites pf WHERE pf.post_id = p.id) AS favorite_count,
	        ` + tipTotalExpr + `
	 FROM posts p
	 JOIN users u ON p.user_id = u.id
	 WHERE ` + where + ` AND ` + visibility + `
	 ORDER BY p.created_at DESC` +
		fmt.Sprintf(" LIMIT $%d OFFSET $%d", len(args)+1, len(args)+2)
	args = append(args, limit, offset)

	rows, err := database.DB.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to search posts: %w", err)
	}
	defer rows.Close()

	posts, postIDs, err := scanPosts(rows)
	if err != nil {
		return nil, err
	}
	if err := r.populateAttachments(posts, postIDs); err != nil {
		return nil, err
	}
	if err := r.populateStats(posts, postIDs); err != nil {
		return nil, err
	}
	if err := r.populateFavorited(posts, postIDs, viewerID); err != nil {
		return nil, err
	}
	if err := populateChecks(posts, postIDs, viewerID); err != nil {
		return nil, err
	}
	if err := populateQuotedPosts(posts, viewerID); err != nil {
		return nil, err
	}
	return posts, nil
}

// ListByUser retrieves paginated posts by a specific user, restricted to posts
// the viewer may see based on their visibility.
func (r *PostRepository) ListByUser(userID int64, page, limit int, viewerID int64) ([]model.Post, error) {
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 20
	}
	offset := (page - 1) * limit

	rows, err := database.DB.Query(
		`SELECT p.id, p.user_id, p.content, p.visibility, p.created_at, p.updated_at, COALESCE(p.quoted_post_id, 0), p.pinned, COALESCE(p.camp_id, 0), u.username, u.nickname, u.avatar_url, u.is_verified`+authorMemberCols+`,
		        (SELECT COUNT(*) FROM post_replies pr WHERE pr.post_id = p.id AND pr.deleted_at IS NULL) AS reply_count,
		        (SELECT COUNT(*) FROM post_favorites pf WHERE pf.post_id = p.id) AS favorite_count,
		        `+tipTotalExpr+`
		 FROM posts p
		 JOIN users u ON p.user_id = u.id
		 WHERE p.user_id = $1 AND `+visibilityCondition(viewerID, "p.", "$2")+`
		 ORDER BY p.pinned DESC, p.created_at DESC
		 LIMIT $3 OFFSET $4`,
		userID, viewerID, limit, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list user posts: %w", err)
	}
	defer rows.Close()

	posts := make([]model.Post, 0)
	var postIDs []int64
	for rows.Next() {
		var post model.Post
		if err := rows.Scan(&post.ID, &post.UserID, &post.Content, &post.Visibility, &post.CreatedAt, &post.UpdatedAt, &post.QuotedPostID, &post.Pinned, &post.CampID, &post.Username, &post.Nickname, &post.AvatarURL, &post.IsVerified, &post.IsBot, &post.MemberLevel, &post.MemberExpiresAt, &post.NameColor, &post.NameColorTo, &post.NameDynamic, &post.NameColors, &post.NameGradientDirection, &post.AvatarFrame, &post.ReplyCount, &post.FavoriteCount, &post.TipTotal); err != nil {
			return nil, fmt.Errorf("failed to scan post: %w", err)
		}
		applyMemberStatus(&post.MemberLevel, &post.MemberExpiresAt, &post.MemberActive)
		posts = append(posts, post)
		postIDs = append(postIDs, post.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}

	if err := r.populateAttachments(posts, postIDs); err != nil {
		return nil, err
	}
	if err := r.populateStats(posts, postIDs); err != nil {
		return nil, err
	}
	if err := r.populateFavorited(posts, postIDs, viewerID); err != nil {
		return nil, err
	}
	if err := populateChecks(posts, postIDs, viewerID); err != nil {
		return nil, err
	}
	if err := populateQuotedPosts(posts, viewerID); err != nil {
		return nil, err
	}
	return posts, nil
}

// ListFavoritePosts retrieves the posts favorited by the given user, most
// recently favorited first.
func (r *PostRepository) ListFavoritePosts(userID int64, page, limit int) ([]model.Post, error) {
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 20
	}
	offset := (page - 1) * limit

	rows, err := database.DB.Query(
		`SELECT p.id, p.user_id, p.content, p.visibility, p.created_at, p.updated_at, COALESCE(p.quoted_post_id, 0), p.pinned, COALESCE(p.camp_id, 0), u.username, u.nickname, u.avatar_url, u.is_verified`+authorMemberCols+`,
		        (SELECT COUNT(*) FROM post_replies pr WHERE pr.post_id = p.id AND pr.deleted_at IS NULL) AS reply_count,
		        (SELECT COUNT(*) FROM post_favorites pf WHERE pf.post_id = p.id) AS favorite_count,
		        `+tipTotalExpr+`
		 FROM posts p
		 JOIN users u ON p.user_id = u.id
		 JOIN post_favorites fv ON fv.post_id = p.id
		 WHERE fv.user_id = $1 AND `+visibilityCondition(userID, "p.", "$1")+`
		 ORDER BY fv.created_at DESC
		 LIMIT $2 OFFSET $3`,
		userID, limit, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list favorite posts: %w", err)
	}
	defer rows.Close()

	posts := make([]model.Post, 0)
	var postIDs []int64
	for rows.Next() {
		var post model.Post
		if err := rows.Scan(&post.ID, &post.UserID, &post.Content, &post.Visibility, &post.CreatedAt, &post.UpdatedAt, &post.QuotedPostID, &post.Pinned, &post.CampID, &post.Username, &post.Nickname, &post.AvatarURL, &post.IsVerified, &post.IsBot, &post.MemberLevel, &post.MemberExpiresAt, &post.NameColor, &post.NameColorTo, &post.NameDynamic, &post.NameColors, &post.NameGradientDirection, &post.AvatarFrame, &post.ReplyCount, &post.FavoriteCount, &post.TipTotal); err != nil {
			return nil, fmt.Errorf("failed to scan post: %w", err)
		}
		applyMemberStatus(&post.MemberLevel, &post.MemberExpiresAt, &post.MemberActive)
		post.Favorited = true
		posts = append(posts, post)
		postIDs = append(postIDs, post.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}

	if err := r.populateAttachments(posts, postIDs); err != nil {
		return nil, err
	}
	if err := r.populateStats(posts, postIDs); err != nil {
		return nil, err
	}
	if err := populateChecks(posts, postIDs, userID); err != nil {
		return nil, err
	}
	return posts, nil
}

// FavoritePost records the user's favorite on a post (idempotent).
func (r *PostRepository) FavoritePost(postID, userID int64) error {
	if _, err := database.DB.Exec(
		"INSERT INTO post_favorites (post_id, user_id) VALUES ($1, $2) ON CONFLICT DO NOTHING",
		postID, userID,
	); err != nil {
		return fmt.Errorf("failed to favorite post: %w", err)
	}
	return nil
}

// UnfavoritePost removes the user's favorite from a post (idempotent).
func (r *PostRepository) UnfavoritePost(postID, userID int64) error {
	if _, err := database.DB.Exec(
		"DELETE FROM post_favorites WHERE post_id = $1 AND user_id = $2",
		postID, userID,
	); err != nil {
		return fmt.Errorf("failed to unfavorite post: %w", err)
	}
	return nil
}

// IsPostFavorited reports whether the user favorited the given post.
func (r *PostRepository) IsPostFavorited(postID, userID int64) (bool, error) {
	var exists bool
	err := database.DB.QueryRow(
		"SELECT EXISTS (SELECT 1 FROM post_favorites WHERE post_id = $1 AND user_id = $2)",
		postID, userID,
	).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("failed to check post favorite: %w", err)
	}
	return exists, nil
}

// populateFavorited marks which of the given posts the viewer favorited.
func (r *PostRepository) populateFavorited(posts []model.Post, postIDs []int64, viewerID int64) error {
	if viewerID <= 0 || len(postIDs) == 0 {
		// Anonymous viewers never see the tip totals either; masking happens
		// unconditionally for anyone other than the author.
		for i := range posts {
			if posts[i].UserID != viewerID {
				posts[i].TipTotal = 0
			}
		}
		return nil
	}
	byID := make(map[int64]*model.Post, len(posts))
	for i := range posts {
		byID[posts[i].ID] = &posts[i]
	}

	rows, err := database.DB.Query(
		"SELECT post_id FROM post_favorites WHERE post_id = ANY($1) AND user_id = $2",
		pq.Array(postIDs), viewerID,
	)
	if err != nil {
		return fmt.Errorf("failed to query post favorites: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var postID int64
		if err := rows.Scan(&postID); err != nil {
			return fmt.Errorf("failed to scan post favorite: %w", err)
		}
		if p, ok := byID[postID]; ok {
			p.Favorited = true
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("rows error: %w", err)
	}
	// Hide tip totals from everyone except the post author. The amounts
	// surface again when the owner views their own posts; everyone else
	// sees a masked card.
	for i := range posts {
		if posts[i].UserID != viewerID {
			posts[i].TipTotal = 0
		}
	}
	return nil
}

func (r *PostRepository) populateAttachments(posts []model.Post, postIDs []int64) error {
	if len(postIDs) == 0 {
		return nil
	}
	attachments, err := r.attachmentRepo.AttachmentsForPosts(postIDs)
	if err != nil {
		return err
	}
	for i := range posts {
		posts[i].Attachments = attachments[posts[i].ID]
	}
	return nil
}

// Post visibility values (mirrors the handler-level constants).
const (
	visibilityPrivate = "private"
	visibilityFriends = "friends"
	visibilityLogin   = "login"
)

// canViewPost mirrors the handler-level visibility rule: public for everyone,
// login for any authenticated user, friends for mutual follows, private for
// the author only. It lives here so quoted-post resolution can mask quotes of
// restricted posts without a handler->repository import cycle.
func canViewPost(post *model.Post, viewerID int64) bool {
	switch post.Visibility {
	case visibilityPrivate:
		return viewerID > 0 && viewerID == post.UserID
	case visibilityFriends:
		if viewerID <= 0 {
			return false
		}
		if viewerID == post.UserID {
			return true
		}
		mutual, err := NewFollowRepository().AreMutual(viewerID, post.UserID)
		return err == nil && mutual
	case visibilityLogin:
		return viewerID > 0
	default:
		return true
	}
}

// populateQuotedPosts resolves the quoted_post reference on every post in
// [posts] one level deep, batched into a single query. Posts whose quoted
// post no longer exists keep the dangling QuotedPostID (clients show a
// "deleted" placeholder); quoted posts the viewer may not see are masked the
// same way so restricted content never leaks through a quote.
func populateQuotedPosts(posts []model.Post, viewerID int64) error {
	if len(posts) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(posts))
	for i := range posts {
		if posts[i].QuotedPostID > 0 {
			ids = append(ids, posts[i].QuotedPostID)
		}
	}
	if len(ids) == 0 {
		return nil
	}

	rows, err := database.DB.Query(
		`SELECT p.id, p.user_id, p.content, p.visibility, p.created_at, p.updated_at, COALESCE(p.quoted_post_id, 0), p.pinned, COALESCE(p.camp_id, 0), u.username, u.nickname, u.avatar_url, u.is_verified`+authorMemberCols+`
		 FROM posts p
		 JOIN users u ON p.user_id = u.id
		 WHERE p.id = ANY($1)`,
		pq.Array(ids),
	)
	if err != nil {
		return fmt.Errorf("failed to load quoted posts: %w", err)
	}
	defer rows.Close()

	byID := make(map[int64]*model.Post, len(ids))
	for rows.Next() {
		var q model.Post
		if err := rows.Scan(&q.ID, &q.UserID, &q.Content, &q.Visibility, &q.CreatedAt, &q.UpdatedAt, &q.QuotedPostID, &q.Pinned, &q.CampID, &q.Username, &q.Nickname, &q.AvatarURL, &q.IsVerified, &q.IsBot, &q.MemberLevel, &q.MemberExpiresAt, &q.NameColor, &q.NameColorTo, &q.NameDynamic, &q.NameColors, &q.NameGradientDirection, &q.AvatarFrame); err != nil {
			return fmt.Errorf("failed to scan quoted post: %w", err)
		}
		applyMemberStatus(&q.MemberLevel, &q.MemberExpiresAt, &q.MemberActive)
		byID[q.ID] = &q
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("rows error: %w", err)
	}

	// Hydrate attachments for the quoted posts (best effort — a quoted post
	// without its media still reads fine).
	quotedIDs := make([]int64, 0, len(byID))
	for id := range byID {
		quotedIDs = append(quotedIDs, id)
	}
	attachments, err := NewAttachmentRepository().AttachmentsForPosts(quotedIDs)
	if err != nil {
		return err
	}
	for id, q := range byID {
		q.Attachments = attachments[id]
	}

	for i := range posts {
		qid := posts[i].QuotedPostID
		if qid <= 0 {
			continue
		}
		if q, ok := byID[qid]; ok && canViewPost(q, viewerID) {
			// One level deep: strip the quoted post's own reference so the
			// payload cannot grow recursively.
			shallow := *q
			shallow.QuotedPostID = 0
			shallow.QuotedPost = nil
			posts[i].QuotedPost = &shallow
		} else {
			posts[i].QuotedPost = nil
		}
	}
	return nil
}

// Delete deletes a post by ID (only by owner). Any non-refunded tips on the
// post are refunded to their senders (debited from the author's wallet) in the
// same transaction, before the ON DELETE CASCADE removes the tip rows.
func (r *PostRepository) Delete(id, userID int64) error {
	tx, err := database.DB.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin post delete: %w", err)
	}
	defer tx.Rollback()

	// Lock the post row first: a concurrent tip must serialize against the
	// refund sweep below (Tip() takes the same row lock).
	var authorID int64
	err = tx.QueryRow("SELECT user_id FROM posts WHERE id = $1 FOR UPDATE", id).Scan(&authorID)
	if err == sql.ErrNoRows {
		return sql.ErrNoRows
	}
	if err != nil {
		return fmt.Errorf("failed to lock post for delete: %w", err)
	}
	if authorID != userID {
		return sql.ErrNoRows
	}

	refunded, err := NewTipRepository().RefundForPostTx(tx, id, authorID)
	if err != nil {
		return err
	}
	if refunded > 0 {
		logger.Log.Info("refunded post tips on delete", "post_id", id, "count", refunded)
	}

	if _, err := tx.Exec("DELETE FROM posts WHERE id = $1", id); err != nil {
		return fmt.Errorf("failed to delete post: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit post delete: %w", err)
	}
	return nil
}

// Update updates a post's content, visibility and attachments (only by owner).
func (r *PostRepository) Update(id, userID int64, content, visibility string, attachmentIDs []int64) (*model.Post, error) {
	result, err := database.DB.Exec(
		"UPDATE posts SET content = $3, visibility = $4, updated_at = NOW() WHERE id = $1 AND user_id = $2",
		id, userID, content, visibility,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to update post: %w", err)
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return nil, sql.ErrNoRows
	}

	if err := r.ReplaceAttachments(id, attachmentIDs); err != nil {
		return nil, err
	}

	return r.GetByID(id)
}

// DeleteAttachmentsForPost removes all attachment links for a post.
func (r *PostRepository) DeleteAttachmentsForPost(postID int64) error {
	if _, err := database.DB.Exec("DELETE FROM post_attachments WHERE post_id = $1", postID); err != nil {
		return fmt.Errorf("failed to delete post attachments: %w", err)
	}
	return nil
}

// ReplaceAttachments replaces the attachments linked to a post.
func (r *PostRepository) ReplaceAttachments(postID int64, attachmentIDs []int64) error {
	if err := r.DeleteAttachmentsForPost(postID); err != nil {
		return err
	}
	if len(attachmentIDs) > 0 {
		if err := r.attachmentRepo.AttachToPost(postID, attachmentIDs); err != nil {
			return err
		}
	}
	return nil
}

func (r *PostRepository) replyCount(postID int64) (int64, error) {
	var count int64
	err := database.DB.QueryRow(
		"SELECT COUNT(*) FROM post_replies WHERE post_id = $1 AND deleted_at IS NULL",
		postID,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("failed to count replies: %w", err)
	}
	return count, nil
}

// ListByCamp retrieves a camp's posts, newest first, with the same
// visibility/attachment/population treatment as the global feed.
func (r *PostRepository) ListByCamp(campID int64, viewerID int64, beforeID int64, limit int) ([]model.Post, error) {
	if limit < 1 {
		limit = 20
	}
	rows, err := database.DB.Query(
		`SELECT p.id, p.user_id, p.content, p.visibility, p.created_at, p.updated_at, COALESCE(p.quoted_post_id, 0), p.pinned, COALESCE(p.camp_id, 0), u.username, u.nickname, u.avatar_url, u.is_verified`+authorMemberCols+`,
		        (SELECT COUNT(*) FROM post_replies pr WHERE pr.post_id = p.id AND pr.deleted_at IS NULL) AS reply_count,
		        (SELECT COUNT(*) FROM post_favorites pf WHERE pf.post_id = p.id) AS favorite_count,
		        `+tipTotalExpr+`
		 FROM posts p
		 JOIN users u ON p.user_id = u.id
		 WHERE p.camp_id = $1 AND `+visibilityCondition(viewerID, "p.", "$2")+`
		 ORDER BY p.created_at DESC
		 LIMIT $3`,
		campID, viewerID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list camp posts: %w", err)
	}
	defer rows.Close()

	posts := make([]model.Post, 0)
	var postIDs []int64
	for rows.Next() {
		var post model.Post
		if err := rows.Scan(&post.ID, &post.UserID, &post.Content, &post.Visibility, &post.CreatedAt, &post.UpdatedAt, &post.QuotedPostID, &post.Pinned, &post.CampID, &post.Username, &post.Nickname, &post.AvatarURL, &post.IsVerified, &post.IsBot, &post.MemberLevel, &post.MemberExpiresAt, &post.NameColor, &post.NameColorTo, &post.NameDynamic, &post.NameColors, &post.NameGradientDirection, &post.AvatarFrame, &post.ReplyCount, &post.FavoriteCount, &post.TipTotal); err != nil {
			return nil, fmt.Errorf("failed to scan post: %w", err)
		}
		applyMemberStatus(&post.MemberLevel, &post.MemberExpiresAt, &post.MemberActive)
		posts = append(posts, post)
		postIDs = append(postIDs, post.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(posts) > 0 {
		if err := r.populateAttachments(posts, postIDs); err != nil {
			return nil, err
		}
		if err := r.populateStats(posts, postIDs); err != nil {
			return nil, err
		}
		if err := r.populateFavorited(posts, postIDs, viewerID); err != nil {
			return nil, err
		}
		if err := populateChecks(posts, postIDs, viewerID); err != nil {
			return nil, err
		}
		if err := populateQuotedPosts(posts, viewerID); err != nil {
			return nil, err
		}
	}
	return posts, nil
}

// SetPinned pins or unpins a post; only the author may change the flag.
// Returns sql.ErrNoRows when the post does not exist or belongs to someone
// else, so the handler can answer 403/404 uniformly.
func (r *PostRepository) SetPinned(postID, userID int64, pinned bool) error {
	res, err := database.DB.Exec(
		"UPDATE posts SET pinned = $3 WHERE id = $1 AND user_id = $2", postID, userID, pinned,
	)
	if err != nil {
		return fmt.Errorf("failed to set post pinned: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}
