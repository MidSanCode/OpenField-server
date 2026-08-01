package service

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/openfield/server/internal/repository"
	"github.com/openfield/server/internal/model"
)

// PostService handles business logic for posts.
type PostService struct {
	postRepo *repository.PostRepository
	userRepo *repository.UserRepository
}

// NewPostService creates a new PostService.
func NewPostService() *PostService {
	return &PostService{
		postRepo: repository.NewPostRepository(),
		userRepo: repository.NewUserRepository(),
	}
}

// CreatePost creates a new post for a user.
func (s *PostService) CreatePost(userID int64, content string, attachmentIDs []int64) (*model.Post, error) {
	if content == "" {
		return nil, errors.New("content cannot be empty")
	}
	if len(content) > 5000 {
		return nil, errors.New("content exceeds maximum length of 5000")
	}
	if len(attachmentIDs) > 9 {
		return nil, errors.New("too many attachments (max 9)")
	}

	post, err := s.postRepo.Create(userID, content, attachmentIDs)
	if err != nil {
		return nil, fmt.Errorf("failed to create post: %w", err)
	}

	user, err := s.userRepo.GetByID(userID)
	if err != nil {
		return nil, fmt.Errorf("failed to get user: %w", err)
	}
	if user != nil {
		post.Username = user.Username
		post.Nickname = user.Nickname
		post.AvatarURL = user.AvatarURL
	}

	return post, nil
}

// GetPosts retrieves paginated posts.
func (s *PostService) GetPosts(page, limit int) ([]model.Post, error) {
	posts, err := s.postRepo.List(page, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to get posts: %w", err)
	}

	userIDs := make([]int64, 0, len(posts))
	for i := range posts {
		userIDs = append(userIDs, posts[i].UserID)
	}

	users, err := s.userRepo.GetUsersByIDs(userIDs)
	if err == nil {
		for i := range posts {
			if user, ok := users[posts[i].UserID]; ok {
				posts[i].Username = user.Username
				posts[i].Nickname = user.Nickname
				posts[i].AvatarURL = user.AvatarURL
			}
		}
	}

	return posts, nil
}

// DeletePost deletes a post if the user is the owner.
func (s *PostService) DeletePost(postID, userID int64) error {
	err := s.postRepo.Delete(postID, userID)
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("post not found or you don't have permission to delete it")
	}
	return err
}

// UpdatePost updates a post's content and attachments if the user is the owner.
func (s *PostService) UpdatePost(postID, userID int64, content string, attachmentIDs []int64) (*model.Post, error) {
	if content == "" {
		return nil, errors.New("content cannot be empty")
	}
	if len(content) > 5000 {
		return nil, errors.New("content exceeds maximum length of 5000")
	}
	if len(attachmentIDs) > 9 {
		return nil, errors.New("too many attachments (max 9)")
	}

	post, err := s.postRepo.Update(postID, userID, content, attachmentIDs)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errors.New("post not found or you don't have permission to edit it")
		}
		return nil, fmt.Errorf("failed to update post: %w", err)
	}

	user, err := s.userRepo.GetByID(userID)
	if err != nil {
		return nil, fmt.Errorf("failed to get user: %w", err)
	}
	if user != nil {
		post.Username = user.Username
		post.Nickname = user.Nickname
		post.AvatarURL = user.AvatarURL
	}

	return post, nil
}

// GetPost retrieves a single post by ID.
func (s *PostService) GetPost(postID int64) (*model.Post, error) {
	post, err := s.postRepo.GetByID(postID)
	if err != nil {
		return nil, fmt.Errorf("failed to get post: %w", err)
	}
	if post == nil {
		return nil, errors.New("post not found")
	}

	user, _ := s.userRepo.GetByID(post.UserID)
	if user != nil {
		post.Username = user.Username
		post.Nickname = user.Nickname
		post.AvatarURL = user.AvatarURL
	}

	return post, nil
}

// GetPostsByUser retrieves posts for a specific user.
func (s *PostService) GetPostsByUser(userID int64, page, limit int) ([]model.Post, error) {
	posts, err := s.postRepo.ListByUser(userID, page, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to get user posts: %w", err)
	}

	user, _ := s.userRepo.GetByID(userID)
	for i := range posts {
		if user != nil {
			posts[i].Username = user.Username
			posts[i].Nickname = user.Nickname
			posts[i].AvatarURL = user.AvatarURL
		}
	}

	return posts, nil
}
