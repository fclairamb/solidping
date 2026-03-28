package checkkafka

import (
	"crypto/sha256"
	"crypto/sha512"
	"hash"

	"github.com/xdg-go/scram"
)

// SHA-256 and SHA-512 hash generator functions for SCRAM authentication.
var (
	sha256HashGenerator scram.HashGeneratorFcn = func() hash.Hash { return sha256.New() }
	sha512HashGenerator scram.HashGeneratorFcn = func() hash.Hash { return sha512.New() }
)

// scramClient implements the sarama.SCRAMClient interface.
type scramClient struct {
	*scram.ClientConversation
	scram.HashGeneratorFcn
}

// Begin starts a new SCRAM conversation.
func (c *scramClient) Begin(userName, password, authzID string) error {
	client, err := c.HashGeneratorFcn.NewClient(userName, password, authzID)
	if err != nil {
		return err
	}

	c.ClientConversation = client.NewConversation()

	return nil
}

// Step processes a server challenge and returns a response.
func (c *scramClient) Step(challenge string) (string, error) {
	return c.ClientConversation.Step(challenge)
}

// Done returns whether the conversation is complete.
func (c *scramClient) Done() bool {
	return c.ClientConversation.Done()
}
