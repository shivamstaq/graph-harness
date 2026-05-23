package resolvers

import (
	"context"
)

type queryResolver struct{}
type mutationResolver struct{}
type subscriptionResolver struct{}

type User struct{}

// User is the resolver for the user field.
func (r *queryResolver) User(ctx context.Context, id string) (*User, error) {
	return nil, nil
}

// Users is the resolver for the users field.
func (r *queryResolver) Users(ctx context.Context) ([]*User, error) {
	return nil, nil
}

// CreateUser is the resolver for the createUser field.
func (r *mutationResolver) CreateUser(ctx context.Context, name string, age *int) (*User, error) {
	return nil, nil
}

// UserAdded is the resolver for the userAdded field.
func (r *subscriptionResolver) UserAdded(ctx context.Context) (<-chan *User, error) {
	return nil, nil
}
