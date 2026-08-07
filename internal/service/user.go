package service

import (
	"errors"

	"github.com/openfield/server/internal/model"
	"github.com/openfield/server/internal/repository"
)

// UserService handles business logic for users.
type UserService struct {
	userRepo *repository.UserRepository
}

// NewUserService creates a new UserService.
func NewUserService() *UserService {
	return &UserService{
		userRepo: repository.NewUserRepository(),
	}
}

// GetUser retrieves a user by ID.
func (s *UserService) GetUser(userID int64) (*model.User, error) {
	return s.userRepo.GetByID(userID)
}

// GetUserByUsername retrieves a user by username.
func (s *UserService) GetUserByUsername(username string) (*model.User, error) {
	return s.userRepo.GetByUsername(username)
}

// GetUserByEmail retrieves a user by email.
func (s *UserService) GetUserByEmail(email string) (*model.User, error) {
	return s.userRepo.GetByEmail(email)
}

// UpdateProfile updates a user's username and nickname, completing registration.
func (s *UserService) UpdateProfile(userID int64, username, nickname string) (*model.User, error) {
	if username == "" {
		return nil, errors.New("username cannot be empty")
	}
	if nickname == "" {
		return nil, errors.New("nickname cannot be empty")
	}
	return s.userRepo.UpdateProfile(userID, username, nickname)
}

// UpdateAvatar updates a user's avatar URL.
func (s *UserService) UpdateAvatar(userID int64, avatarURL string) error {
	if avatarURL == "" {
		return errors.New("avatar URL cannot be empty")
	}
	return s.userRepo.UpdateAvatar(userID, avatarURL)
}

// UpdateBanner updates a user's banner URL.
func (s *UserService) UpdateBanner(userID int64, bannerURL string) error {
	if bannerURL == "" {
		return errors.New("banner URL cannot be empty")
	}
	return s.userRepo.UpdateBanner(userID, bannerURL)
}

// SetPassword sets a user's password hash.
func (s *UserService) SetPassword(userID int64, passwordHash string) error {
	if passwordHash == "" {
		return errors.New("password hash cannot be empty")
	}
	return s.userRepo.SetPassword(userID, passwordHash)
}
