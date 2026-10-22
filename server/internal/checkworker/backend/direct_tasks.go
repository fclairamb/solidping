package backend

import (
	"context"
	"encoding/json"

	"github.com/fclairamb/solidping/server/internal/workertasks"
)

// DirectTaskClient is the in-process worker-task client of a cloud worker. It
// calls the same service, with the same claim path and fencing, as the
// agents' HTTP routes.
type DirectTaskClient struct {
	svc      *workertasks.Service
	identity func() workertasks.WorkerIdentity
}

// NewDirectTaskClient builds the client. identity is read on every claim: the
// worker's uid is only known once it registered.
func NewDirectTaskClient(svc *workertasks.Service, identity func() workertasks.WorkerIdentity) *DirectTaskClient {
	return &DirectTaskClient{svc: svc, identity: identity}
}

// Claim implements tasks.Client.
func (c *DirectTaskClient) Claim(ctx context.Context, kinds []string) (*workertasks.Claim, error) {
	identity := c.identity()
	identity.Kinds = kinds

	return c.svc.Claim(ctx, &identity)
}

// Heartbeat implements tasks.Client.
func (c *DirectTaskClient) Heartbeat(ctx context.Context, uid string) error {
	_, err := c.svc.Heartbeat(ctx, uid, c.identity().UID)

	return err
}

// Step implements tasks.Client.
func (c *DirectTaskClient) Step(ctx context.Context, uid string, req workertasks.StepRequest) error {
	return c.svc.Step(ctx, uid, c.identity().UID, req)
}

// Finish implements tasks.Client.
func (c *DirectTaskClient) Finish(ctx context.Context, uid string, body json.RawMessage) error {
	return c.svc.Finish(ctx, uid, c.identity().UID, body)
}

// Fail implements tasks.Client.
func (c *DirectTaskClient) Fail(ctx context.Context, uid, errorCode string) error {
	return c.svc.FailSlice(ctx, uid, c.identity().UID, errorCode)
}
