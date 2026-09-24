package provider

import "context"

type ILoader interface {
	Load(ctx context.Context) (Groups, error)
	Close() error
}
